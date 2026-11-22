package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config consumes the same config/server.json and optional config/local.json as the legacy host.
type Config struct {
	Root   string `json:"-"`
	Server struct {
		Address                                                      string
		ChannelPort, GamePort, MaximumConnections, MaximumFrameBytes int
		AdditionalGameChannels                                       map[uint16]int
	}
	Gameplay struct {
		LocalAccountEnabled                                bool
		NativeTownProfile140, ClientDelegatedCombatEnabled bool
		NativePartyNotices                                 *bool
		ChannelScript                                      string
	}
	Resources struct{ Pvf, ClientProfile string }
	Lifecycle struct{ StopFile string }
}

func Load(root string) (Config, error) {
	var c Config
	root, err := filepath.Abs(root)
	if err != nil {
		return c, err
	}
	c.Root = root
	c.Server.Address = "127.0.0.1"
	c.Server.ChannelPort, c.Server.GamePort = 7001, 10019
	c.Server.MaximumConnections, c.Server.MaximumFrameBytes = 16, 1<<20
	c.Server.AdditionalGameChannels = make(map[uint16]int)
	for _, name := range []string{"server.json", "local.json"} {
		path := filepath.Join(root, "config", name)
		data, err := os.ReadFile(path)
		if name == "local.json" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return c, err
		}
		if err = json.Unmarshal([]byte(strings.TrimPrefix(string(data), "\ufeff")), &c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		key, found := strings.CutPrefix(strings.ToUpper(name), "SERVE100_NEXT_")
		if !found {
			continue
		}
		switch key {
		case "SERVER__ADDRESS":
			c.Server.Address = value
		case "SERVER__CHANNELPORT", "SERVER__GAMEPORT", "SERVER__MAXIMUMCONNECTIONS", "SERVER__MAXIMUMFRAMEBYTES":
			n, e := strconv.Atoi(value)
			if e != nil {
				return c, fmt.Errorf("%s must be an integer", name)
			}
			switch key {
			case "SERVER__CHANNELPORT":
				c.Server.ChannelPort = n
			case "SERVER__GAMEPORT":
				c.Server.GamePort = n
			case "SERVER__MAXIMUMCONNECTIONS":
				c.Server.MaximumConnections = n
			case "SERVER__MAXIMUMFRAMEBYTES":
				c.Server.MaximumFrameBytes = n
			}
		case "GAMEPLAY__LOCALACCOUNTENABLED", "GAMEPLAY__NATIVEPARTYNOTICES", "GAMEPLAY__NATIVETOWNPROFILE140", "GAMEPLAY__CLIENTDELEGATEDCOMBATENABLED":
			v, e := strconv.ParseBool(value)
			if e != nil {
				return c, e
			}
			switch key {
			case "GAMEPLAY__LOCALACCOUNTENABLED":
				c.Gameplay.LocalAccountEnabled = v
			case "GAMEPLAY__NATIVETOWNPROFILE140":
				c.Gameplay.NativeTownProfile140 = v
			case "GAMEPLAY__CLIENTDELEGATEDCOMBATENABLED":
				c.Gameplay.ClientDelegatedCombatEnabled = v
			default:
				c.Gameplay.NativePartyNotices = &v
			}
		case "GAMEPLAY__CHANNELSCRIPT":
			c.Gameplay.ChannelScript = value
		case "RESOURCES__PVF":
			c.Resources.Pvf = value
		case "RESOURCES__CLIENTPROFILE":
			c.Resources.ClientProfile = value
		case "LIFECYCLE__STOPFILE":
			c.Lifecycle.StopFile = value
		default:
			if id, ok := strings.CutPrefix(key, "SERVER__ADDITIONALGAMECHANNELS__"); ok {
				channel, e := strconv.ParseUint(id, 10, 16)
				if e != nil {
					return c, e
				}
				port, e := strconv.Atoi(value)
				if e != nil {
					return c, e
				}
				if c.Server.AdditionalGameChannels == nil {
					c.Server.AdditionalGameChannels = make(map[uint16]int)
				}
				c.Server.AdditionalGameChannels[uint16(channel)] = port
			}
		}
	}
	return c, nil
}

func (c Config) Path(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(c.Root, filepath.FromSlash(path))
}

func (c Config) StopPath() (string, error) {
	p := c.Path(c.Lifecycle.StopFile)
	rel, err := filepath.Rel(filepath.Join(c.Root, "runtime"), p)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return "", errors.New("Lifecycle.StopFile must be under runtime")
	}
	return p, nil
}

func (c Config) GamePorts() (map[uint16]int, error) {
	s := c.Server
	if !c.Gameplay.LocalAccountEnabled {
		return nil, errors.New("the native player entry requires local account mode")
	}
	if ip := net.ParseIP(s.Address); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("uncredentialed local entry must bind loopback")
	}
	if s.MaximumConnections < 1 || s.MaximumConnections > 128 || s.MaximumFrameBytes < 16 || s.MaximumFrameBytes > 1<<20 {
		return nil, errors.New("invalid transport limits")
	}
	ports := map[uint16]int{19: s.GamePort}
	for _, row := range []struct {
		id   uint16
		port int
	}{{251, 10501}, {252, 10252}, {63, 10063}} {
		p := row.port
		if s.GamePort == 0 {
			p = 0
		}
		ports[row.id] = p
	}
	for id, p := range s.AdditionalGameChannels {
		if id == 501 {
			if _, also := s.AdditionalGameChannels[251]; also {
				return nil, errors.New("both free-duel IDs were configured")
			}
			id = 251
		}
		if id != 251 && id != 252 && id != 63 {
			return nil, fmt.Errorf("unsupported additional channel %d", id)
		}
		ports[id] = p
	}
	if s.ChannelPort < 0 || s.ChannelPort > 65535 {
		return nil, errors.New("invalid channel port")
	}
	seen := map[int]bool{s.ChannelPort: true}
	for _, p := range ports {
		if p < 0 || p > 65535 || p != 0 && seen[p] {
			return nil, errors.New("channel and game listeners must have valid, distinct ports")
		}
		seen[p] = true
	}
	return ports, nil
}
