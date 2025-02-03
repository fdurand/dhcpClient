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
	Name     string
	intNet   *net.Interface
	ServerIP net.IP
	Renew    time.Duration
}

// Options Struct
type Options struct {
	Option dhcp4.OptionCode `json:"option"`
	Value  string           `json:"value"`
	Type   string           `json:"type"`
}

func (d *Interface) readConfig() {

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

	Renew := cfg.Section("global").Key("renew").String()
	timeout, err := strconv.Atoi(Renew)
	if err != nil {
		fmt.Printf("Fail to parse renew timeout:%v, %v", Renew, err)
		os.Exit(1)
	}
	d.Renew = time.Duration(timeout) * time.Second
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
	d.readConfig()

	// Random xid
	xid := make([]byte, 4)
	rand.Read(xid)

	// Add options
	var options = Options{}

	// Read options from json file
	dhcpOptions := options.ReadOptions()

	// Request IP address
	packet := dhcp4.RequestPacket(dhcp4.Request, d.intNet.HardwareAddr, net.IPv4(0, 0, 0, 0), xid, true, dhcpOptions)

	Client, err := NewRawClient(d.intNet)
	if err != nil {
		fmt.Printf("Error : %s", err)
		panic(err)
	}
	broadcastMAC, err := net.ParseMAC("FF:FF:FF:FF:FF:FF")
	if err != nil {
		fmt.Printf("Error : %s", err)
		panic(err)
	}
	for {
		err = Client.sendDHCP(broadcastMAC, packet, d.ServerIP, net.IPv4zero)
		if err != nil {
			fmt.Printf("Error : %s", err)
			panic(err)
		}
		time.Sleep(d.Renew)
	}

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
