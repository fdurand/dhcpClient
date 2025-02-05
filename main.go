package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/daemon"
	"github.com/krolaw/dhcp4"
	"gopkg.in/ini.v1"
)

type Interface struct {
	Name      string
	intNet    *net.Interface
	ServerIP  net.IP           // DHCP Server IP (Destination IP)
	DstMac    net.HardwareAddr // Server Destination MAC (Ethernet Header)
	Renew     time.Duration    // Renewal time
	ClientMAC net.HardwareAddr // Device Client MAC (In the DHCP Request)
	GiAddr    net.IP           // Source Gateway IP
	SrcMac    net.HardwareAddr // Source MAC (Ethernet Header)
	CiAddr    net.IP           // Client IP (Requesting IP)

}

type Upnp struct {
	intNet     *net.Interface
	UserAgent  string
	IPAddr     net.IP
	UDPPort    int
	deviceType string
}

// Options Struct
type Options struct {
	Option dhcp4.OptionCode `json:"option"`
	Value  string           `json:"value"`
	Type   string           `json:"type"`
}

func (d *Interface) readDhcpConfig() {

	cfg, err := ini.Load("/usr/local/etc/godhcpclient.ini")
	if err != nil {
		fmt.Printf("Fail to read file: %v", err)
		os.Exit(1)
	}

	Interface := cfg.Section("global").Key("interface").String()
	d.intNet, err = net.InterfaceByName(Interface)
	if err != nil {
		fmt.Printf("Fail to find network interface:%v, %v", Interface, err)
		os.Exit(1)
	}

	Server := cfg.Section("global").Key("server").String()
	d.ServerIP = net.ParseIP(Server)

	GiAddr := cfg.Section("global").Key("giaddr").String()
	d.GiAddr = net.ParseIP(GiAddr)

	if d.GiAddr == nil {
		d.GiAddr = net.IPv4zero
	}

	CiAddr := cfg.Section("global").Key("ciaddr").String()
	d.CiAddr = net.ParseIP(CiAddr)

	if d.CiAddr == nil {
		d.CiAddr = net.IPv4zero
	}

	ClientMac := cfg.Section("global").Key("clientmac").String()
	d.ClientMAC, err = net.ParseMAC(ClientMac)

	if err != nil {
		d.ClientMAC, err = net.ParseMAC("00:00:00:00:00:00")
	}

	SrcMac := cfg.Section("global").Key("srcmac").String()
	d.SrcMac, err = net.ParseMAC(SrcMac)

	if err != nil {
		d.SrcMac = d.intNet.HardwareAddr
	}

	DstMac := cfg.Section("global").Key("dstmac").String()
	d.DstMac, err = net.ParseMAC(DstMac)

	if err != nil {
		d.DstMac, err = net.ParseMAC("FF:FF:FF:FF:FF:FF")
	}

	Renew := cfg.Section("global").Key("renew").String()
	timeout, err := strconv.Atoi(Renew)
	if err != nil {
		fmt.Printf("Fail to parse renew timeout:%v, %v", Renew, err)
		os.Exit(1)
	}
	d.Renew = time.Duration(timeout) * time.Second
}

func (u *Upnp) readUpnpConfig() {

	cfg, err := ini.Load("/usr/local/etc/goupnp.ini")
	if err != nil {
		fmt.Printf("Fail to read file: %v", err)
		os.Exit(1)
	}

	Interface := cfg.Section("global").Key("interface").String()
	u.intNet, err = net.InterfaceByName(Interface)
	if err != nil {
		fmt.Printf("Fail to find network interface:%v, %v", Interface, err)
		os.Exit(1)
	}

	UserAgent := cfg.Section("global").Key("useragent").String()
	u.UserAgent = UserAgent

	ipaddr := cfg.Section("global").Key("ipaddr").String()
	u.IPAddr = net.ParseIP(ipaddr)

	udpport := cfg.Section("global").Key("udpport").String()
	UdpPort, err := strconv.Atoi(udpport)
	if err != nil {
		fmt.Printf("Fail to parse udp port:%v, %v", udpport, err)
		os.Exit(1)
	}
	u.UDPPort = UdpPort

	deviceType := cfg.Section("global").Key("devicetype").String()
	u.deviceType = deviceType
}

func main() {

	go func() {
		// Systemd
		daemon.SdNotify(false, "READY=1")
		interval, err := daemon.SdWatchdogEnabled(false)
		if err != nil || interval == 0 {
			return
		}
		for {
			daemon.SdNotify(false, "WATCHDOG=1")
			time.Sleep(interval / 3)
		}
	}()

	var d Interface
	d.readDhcpConfig()

	var u Upnp
	u.readUpnpConfig()

	go func() {
		for {
			u.discover(time.Second * 10)

			time.Sleep(time.Second * 30)
		}
	}()

	// Random xid
	xid := make([]byte, 4)
	rand.Read(xid)

	// Add options
	var options = Options{}

	// Read options from json file
	dhcpOptions := options.ReadOptions()

	broadcast := false
	if d.DstMac.String() == "FF:FF:FF:FF:FF:FF" {
		broadcast = true
	}

	// Request IP address

	packet := RequestPacket(dhcp4.Request, d.ClientMAC, d.GiAddr, d.CiAddr, xid, broadcast, dhcpOptions)

	Client, err := NewRawClient(d.intNet)
	if err != nil {
		fmt.Printf("Error : %s", err)
		panic(err)
	}

	for {
		err = Client.sendDHCP(d.DstMac, d.SrcMac, packet, d.ServerIP, d.GiAddr)
		if err != nil {
			fmt.Printf("Error : %s", err)
			panic(err)
		}
		time.Sleep(d.Renew)
	}

}

// Creates a request packet that a Client would send to a server.
func RequestPacket(mt dhcp4.MessageType, chAddr net.HardwareAddr, giAddr net.IP, cIAddr net.IP, xId []byte, broadcast bool, options []dhcp4.Option) dhcp4.Packet {
	p := dhcp4.NewPacket(dhcp4.BootRequest)
	p.SetCHAddr(chAddr)
	p.SetXId(xId)
	if cIAddr != nil {
		p.SetCIAddr(cIAddr)
	}
	p.SetGIAddr(giAddr)
	p.SetBroadcast(broadcast)
	p.AddOption(dhcp4.OptionDHCPMessageType, []byte{byte(mt)})
	for _, o := range options {
		p.AddOption(o.Code, o.Value)
	}
	p.PadToMinSize()
	return p
}

func (a *Options) ReadOptions() []dhcp4.Option {

	DHCPOptions := []Options{}
	var dhcpOptions = []dhcp4.Option{}

	body, err := os.ReadFile("/usr/local/etc/Options.json")
	if err != nil {
		fmt.Printf("Error : %s", err)
		panic(err)
	}
	err = json.Unmarshal(body, &DHCPOptions)
	if err != nil {
		fmt.Printf("Error : %s", err)
		panic(err)
	}
	for _, option := range DHCPOptions {
		var dhcpOption = dhcp4.Option{}
		var Value interface{}
		switch option.Type {
		case "ipaddr":
			Value = net.ParseIP(option.Value)
			dhcpOption.Code = option.Option
			dhcpOption.Value = Value.(net.IP).To4()
		case "string":
			Value = option.Value
			dhcpOption.Code = option.Option
			dhcpOption.Value = []byte(Value.(string))
		case "int":
			Value = option.Value
			val, _ := strconv.Atoi(Value.(string))
			bs := make([]byte, 4)
			binary.BigEndian.PutUint32(bs, uint32(val))
			dhcpOption.Code = option.Option
			dhcpOption.Value = bs
		case "bytes":
			dhcpOption.Code = option.Option
			for _, value := range strings.Split(option.Value, ",") {
				val, _ := strconv.Atoi(value)
				dhcpOption.Value = append(dhcpOption.Value, byte(val))
			}
		}
		dhcpOptions = append(dhcpOptions, dhcpOption)
	}
	return dhcpOptions
}

func (u *Upnp) discover(timeout time.Duration) {
	ssdp := &net.UDPAddr{IP: u.IPAddr, Port: u.UDPPort}

	tpl := `M-SEARCH * HTTP/1.1
HOST: %s
ST: %s
MAN: "ssdp:discover"
MX: %d
USER-AGENT: %s

`
	searchStr := fmt.Sprintf(tpl, ssdp, u.deviceType, timeout/time.Second, u.UserAgent)

	search := []byte(strings.Replace(searchStr, "\n", "\r\n", -1))

	fmt.Println("Starting discovery of device type", u.deviceType, "on", u.intNet.Name)

	socket, err := net.ListenMulticastUDP("udp4", u.intNet, &net.UDPAddr{IP: ssdp.IP})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer socket.Close() // Make sure our socket gets closed

	err = socket.SetDeadline(time.Now().Add(timeout))
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("Sending search request for device type", u.deviceType, "on", u.intNet.Name)

	_, err = socket.WriteTo(search, ssdp)
	if err != nil {
		if e, ok := err.(net.Error); !ok || !e.Timeout() {
			fmt.Println(err)
		}
		return
	}

	fmt.Println("Listening for UPnP response for device type", u.deviceType, "on", u.intNet.Name)

	// Listen for responses until a timeout is reached
	for {
		resp := make([]byte, 65536)
		_, _, err := socket.ReadFrom(resp)
		if err != nil {
			if e, ok := err.(net.Error); !ok || !e.Timeout() {
				fmt.Println("UPnP read:", err) //legitimate error, not a timeout.
			}
			break
		}
	}
	fmt.Println("Discovery for device type", u.deviceType, "on", u.intNet.Name, "finished.")
}
