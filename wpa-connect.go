package wpaconnect

import (
	"errors"
	"fmt"
	"github.com/godbus/dbus"
	"github.com/mark2b/wpa-connect/internal/log"
	"github.com/mark2b/wpa-connect/internal/wpa_cli"
	"github.com/mark2b/wpa-connect/internal/wpa_dbus"
	"net"
	"time"
)

func (self *connectManager) GetCurrentNetwork() ConnectionInfo {
	self.deadTime = time.Now().Add(5 * time.Second)
	self.context = &connectContext{}
	self.context.scanDone = make(chan bool)
	self.context.connectDone = make(chan bool)
	if wpa, err := wpa_dbus.NewWPA(); err == nil {
		wpa.WaitForSignals(self.onSignal)
		wpa.AddSignalsObserver()
		if wpa.ReadInterface(self.NetInterface); wpa.Error == nil {
			iface := wpa.Interface
			iface.AddSignalsObserver()
			iface.ReadCurrentNetwork()
			iface.CurrentNetwork.ReadProperties()
			self.readNetAddress()
			return ConnectionInfo{NetInterface: self.NetInterface, SSID: iface.CurrentNetwork.SSID,
				IP4: self.context.ip4, IP6: self.context.ip6}
		}
		wpa.RemoveSignalsObserver()
		wpa.StopWaitForSignals()
	}
	return ConnectionInfo{}
}

func (self *connectManager) PreAuthenticate(request ConnectionRequest, timeout time.Duration) (e error) {
	self.deadTime = time.Now().Add(timeout)
	self.context = &connectContext{}
	self.context.scanDone = make(chan bool)
	self.context.connectDone = make(chan bool)
	if wpa, err := wpa_dbus.NewWPA(); err == nil {
		wpa.WaitForSignals(self.onSignal)
		wpa.AddSignalsObserver()
		if wpa.ReadInterface(self.NetInterface); wpa.Error == nil {
			iface := wpa.Interface
			iface.AddSignalsObserver()
			self.context.phaseWaitForScanDone = true
			go func() {
				time.Sleep(self.deadTime.Sub(time.Now()))
				self.context.scanDone <- false
				self.context.error = errors.New("scan timeout")
			}()

			iface.ReadCurrentNetwork()
			previousNetwork := *iface.CurrentNetwork
			previousNetwork.ReadProperties()
			iface.Disconnect()
			if iface.Scan(); iface.Error == nil {
				// Wait for scan done
				if <-self.context.scanDone; self.context.error == nil {
					if iface.ReadBSSList(); iface.Error == nil {
						bssMap := make(map[string]wpa_dbus.BSSWPA, 0)
						for _, bss := range iface.BSSs {
							if bss.ReadSSID().ReadRSN().ReadWPA(); bss.Error == nil {
								bssMap[bss.SSID] = bss
								log.Log.Debug(bss.SSID, bss.BSSID)
							} else {
								e = err
								break
							}
						}
						if e == nil {
							bss := bssMap[request.SSID]
							if err := self.connectToBSS(
								&bss,
								iface, request, false); err != nil {
								e = err
							}
							func() {
								self.deadTime = time.Now().Add(timeout)
								self.context.error = nil
								self.context.phaseWaitForInterfaceConnected = true
								// remove the temporary network we tried to pre-auth
								iface.NewNetwork.Remove()
								if previousNetwork.Select(); previousNetwork.Error == nil {
									if connected := <-self.context.connectDone; self.context.error == nil {
										if connected {
											if err := self.readNetAddress(); err != nil {
												e = err
											}
										} else {
											if iface.ReadDisconnectReason(); iface.Error == nil {
												e = errors.New(fmt.Sprintf("connection_failed, reason=%d", iface.DisconnectReason))
											} else {
												e = errors.New("connection_failed")
											}
										}
									} else {
										e = self.context.error
									}
								} else {
									e = previousNetwork.Error
								}
							}()

						}
					} else {
						e = iface.Error
					}
				} else {
					e = self.context.error
				}
			} else {
				e = wpa.Error
			}
			iface.RemoveSignalsObserver()
		} else {
			e = wpa.Error
		}
		wpa.RemoveSignalsObserver()
		wpa.StopWaitForSignals()
	} else {
		e = err
	}
	return
}

func (self *connectManager) Connect(request ConnectionRequest, timeout time.Duration) (connectionInfo ConnectionInfo, e error) {
	self.deadTime = time.Now().Add(timeout)
	self.context = &connectContext{}
	self.context.scanDone = make(chan bool)
	self.context.connectDone = make(chan bool)
	if wpa, err := wpa_dbus.NewWPA(); err == nil {
		wpa.WaitForSignals(self.onSignal)
		wpa.AddSignalsObserver()
		if wpa.ReadInterface(self.NetInterface); wpa.Error == nil {
			iface := wpa.Interface
			iface.AddSignalsObserver()
			self.context.phaseWaitForScanDone = true
			go func() {
				time.Sleep(self.deadTime.Sub(time.Now()))
				self.context.scanDone <- false
				self.context.error = errors.New("timeout")
			}()
			if iface.Scan(); iface.Error == nil {
				// Wait for scan done
				if <-self.context.scanDone; self.context.error == nil {
					if iface.ReadBSSList(); iface.Error == nil {
						bssMap := make(map[string]wpa_dbus.BSSWPA, 0)
						for _, bss := range iface.BSSs {
							if bss.ReadSSID().ReadWPA().ReadRSN(); bss.Error == nil {
								bssMap[bss.SSID] = bss
								log.Log.Debug(bss.SSID, bss.BSSID)
							} else {
								e = err
								break
							}
						}
						if e == nil {
							bss := bssMap[request.SSID]
							if err := self.connectToBSS(
								&bss, iface, request, true); err == nil {
								// Connected, save configuration
								cli := wpa_cli.WPACli{NetInterface: self.NetInterface}
								if err := cli.SaveConfig(); err != nil {
									e = err
								}
								connectionInfo = ConnectionInfo{NetInterface: self.NetInterface, SSID: request.SSID,
									IP4: self.context.ip4, IP6: self.context.ip6}
							} else {
								e = err
							}
						}
					} else {
						e = iface.Error
					}
				} else {
					e = self.context.error
				}
			} else {
				e = wpa.Error
			}
			iface.RemoveSignalsObserver()
		} else {
			e = wpa.Error
		}
		wpa.RemoveSignalsObserver()
		wpa.StopWaitForSignals()
	} else {
		e = err
	}
	return
}

func (self *connectManager) SaveConfig() error {
	wpaCli := wpa_cli.WPACli{NetInterface: self.NetInterface}
	return wpaCli.SaveConfig()
}

func (self *connectManager) connectToBSS(bss *wpa_dbus.BSSWPA, iface *wpa_dbus.InterfaceWPA, connectionRequest ConnectionRequest, removeAllPreviousNetwork bool) (e error) {
	addNetworkArgs := map[string]dbus.Variant{
		"ssid": dbus.MakeVariant(bss.SSID),
	}
	if connectionRequest.Hidden {
		addNetworkArgs["scan_ssid"] = dbus.MakeVariant(1)
	}
	if connectionRequest.Password == "" {
		addNetworkArgs["key_mgmt"] = dbus.MakeVariant("NONE")
	} else {
		if bss.RSNKeyMgmt[0] == "psk" {
			addNetworkArgs["psk"] = dbus.MakeVariant(connectionRequest.Password)
		}

		if bss.RSNKeyMgmt[0] == "wpa-eap" {
			addNetworkArgs["key_mgmt"] = dbus.MakeVariant("WPA-EAP")
			addNetworkArgs["identity"] = dbus.MakeVariant(connectionRequest.Identity)
			addNetworkArgs["password"] = dbus.MakeVariant(connectionRequest.Password)
			addNetworkArgs["eap"] = dbus.MakeVariant(connectionRequest.EAP)
			addNetworkArgs["pairwise"] = dbus.MakeVariant(connectionRequest.Pairwise)
			addNetworkArgs["phase1"] = dbus.MakeVariant(connectionRequest.Phase1)
			addNetworkArgs["phase2"] = dbus.MakeVariant(connectionRequest.Phase2)
		}
	}
	if removeAllPreviousNetwork {
		iface.RemoveAllNetworks()
	}
	if iface.AddNetwork(addNetworkArgs); iface.Error == nil {
		network := iface.NewNetwork
		self.context.phaseWaitForInterfaceConnected = true
		go func() {
			time.Sleep(self.deadTime.Sub(time.Now()))
			self.context.connectDone <- false
			self.context.error = errors.New("timeout")
		}()
		if network.Select(); network.Error == nil {
			if connected := <-self.context.connectDone; self.context.error == nil {
				if connected {
					if err := self.readNetAddress(); err == nil {
					} else {
						e = err
					}
				} else {
					if iface.ReadDisconnectReason(); iface.Error == nil {
						e = errors.New(fmt.Sprintf("connection_failed, reason=%d", iface.DisconnectReason))
					} else {
						e = errors.New("connection_failed")
					}
				}
			} else {
				e = self.context.error
			}
		} else {
			e = network.Error
		}
	} else {
		e = iface.Error
	}
	return
}

func (self *connectManager) onSignal(wpa *wpa_dbus.WPA, signal *dbus.Signal) {
	log.Log.Debug(signal.Name, signal.Path)
	switch signal.Name {
	case "fi.w1.wpa_supplicant1.Interface.BSSAdded":
	case "fi.w1.wpa_supplicant1.Interface.BSSRemoved":
		break
	case "fi.w1.wpa_supplicant1.Interface.ScanDone":
		self.processScanDone(wpa, signal)
	case "fi.w1.wpa_supplicant1.Interface.PropertiesChanged":
		log.Log.Debug(signal.Name, signal.Path, signal.Body)
		self.processInterfacePropertiesChanged(wpa, signal)
	default:
		log.Log.Debug(signal.Name, signal.Path, signal.Body)
	}
}

func (self *connectManager) readNetAddress() (e error) {
	if netIface, err := net.InterfaceByName(self.NetInterface); err == nil {
		for time.Now().Before(self.deadTime) && !self.context.hasIP() {
			if addrs, err := netIface.Addrs(); err == nil {
				for _, addr := range addrs {
					if ip, _, err := net.ParseCIDR(addr.String()); err == nil {
						if self.context.ip4 == nil {
							self.context.ip4 = ip.To4()
							continue
						}
						if self.context.ip6 == nil {
							self.context.ip6 = ip.To16()
							continue
						}
					} else {
						e = err
						return
					}
				}
			} else {
				e = err
			}
			time.Sleep(time.Millisecond * 500)
		}
		if !self.context.hasIP() {
			e = errors.New("address_not_allocated")
		}
	} else {
		e = err
	}
	return
}

func (self *connectManager) processScanDone(wpa *wpa_dbus.WPA, signal *dbus.Signal) {
	log.Log.Debug("processScanDone")
	if self.context.phaseWaitForScanDone {
		self.context.phaseWaitForScanDone = false
		self.context.scanDone <- true
	}
}

func (self *connectManager) processInterfacePropertiesChanged(wpa *wpa_dbus.WPA, signal *dbus.Signal) {
	log.Log.Debug("processInterfacePropertiesChanged")
	log.Log.Debug("phaseWaitForInterfaceConnected", self.context.phaseWaitForInterfaceConnected)
	if self.context.phaseWaitForInterfaceConnected {
		if len(signal.Body) > 0 {
			properties := signal.Body[0].(map[string]dbus.Variant)
			if stateVariant, hasState := properties["State"]; hasState {
				if state, ok := stateVariant.Value().(string); ok {
					log.Log.Debug("State", state)
					if state == "completed" {
						self.context.phaseWaitForInterfaceConnected = false
						self.context.connectDone <- true
						return
					} else if state == "disconnected" {
						// self.context.phaseWaitForInterfaceConnected = false
						// self.context.connectDone <- false
						return
					}
				}
			}
		}
	}
}

func (self *connectContext) hasIP() bool {
	return self.ip4 != nil && self.ip6 != nil
}

func NewConnectManager(netInterface string) *connectManager {
	return &connectManager{NetInterface: netInterface}
}

type ConnectionInfo struct {
	NetInterface string
	SSID         string
	IP4          net.IP
	IP6          net.IP
}

type connectContext struct {
	phaseWaitForScanDone           bool
	phaseWaitForInterfaceConnected bool
	scanDone                       chan bool
	connectDone                    chan bool
	ip4                            net.IP
	ip6                            net.IP
	error                          error
}

type connectManager struct {
	context      *connectContext
	deadTime     time.Time
	NetInterface string
}

var (
	ConnectManager = &connectManager{NetInterface: "wlan0"}
)

type ConnectionRequest struct {
	SSID     string
	Identity string
	Password string
	Hidden   bool
	EAP      string
	Pairwise string
	Phase1   string
	Phase2   string
}
