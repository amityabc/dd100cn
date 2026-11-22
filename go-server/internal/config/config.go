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
	"time"
)

// Config consumes the same config/server.json and optional config/local.json as the legacy host.
type Config struct {
	Root   string `json:"-"`
	Server struct {
		Address                                                                          string
		ChannelPort, GamePort, MaximumConnections, MaximumFrameBytes                     int
		ReceiveChunkBytes, ReceiveQueueCapacity, SendQueueCapacity, SessionQueueCapacity int
		AdditionalGameChannels                                                           map[uint16]int
	}
	Gameplay struct {
		ExperienceMultiplier                               int
		LocalAccountEnabled                                bool
		NativeTownProfile140, ClientDelegatedCombatEnabled bool
		NativePartyNotices                                 *bool
		ChannelScript                                      string
		ClientDirectory, Launcher                          string
		GmAccounts                                         []string
		BattlePassStartsAt, BattlePassEndsAt               *time.Time
	}
	Resources    struct{ Pvf, ClientProfile, PvfSha256, Probe string }
	Storage      struct{ Database string }
	Lifecycle    struct{ StopFile string }
	Diagnostics  struct{ Frames, FrameMode string }
	Verification struct{ EvidenceRoot string }
	TestModules  struct{ CounterEnabled, SharedBoundaryEnabled bool }
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
	c.Server.ReceiveChunkBytes, c.Server.ReceiveQueueCapacity = 8192, 16
	c.Server.SendQueueCapacity, c.Server.SessionQueueCapacity = 8, 16
	c.Gameplay.ExperienceMultiplier = 1
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
	gmBaseCount := len(c.Gameplay.GmAccounts)
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		key, found := strings.CutPrefix(strings.ToUpper(name), "SERVE100_NEXT_")
		if !found {
			continue
		}
		switch key {
		case "SERVER__ADDRESS":
			c.Server.Address = value
		case "SERVER__CHANNELPORT", "SERVER__GAMEPORT", "SERVER__MAXIMUMCONNECTIONS", "SERVER__MAXIMUMFRAMEBYTES",
			"SERVER__RECEIVECHUNKBYTES", "SERVER__RECEIVEQUEUECAPACITY", "SERVER__SENDQUEUECAPACITY", "SERVER__SESSIONQUEUECAPACITY":
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
			case "SERVER__RECEIVECHUNKBYTES":
				c.Server.ReceiveChunkBytes = n
			case "SERVER__RECEIVEQUEUECAPACITY":
				c.Server.ReceiveQueueCapacity = n
			case "SERVER__SENDQUEUECAPACITY":
				c.Server.SendQueueCapacity = n
			case "SERVER__SESSIONQUEUECAPACITY":
				c.Server.SessionQueueCapacity = n
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
		case "GAMEPLAY__EXPERIENCEMULTIPLIER":
			n, e := strconv.Atoi(value)
			if e != nil {
				return c, fmt.Errorf("%s must be an integer", name)
			}
			c.Gameplay.ExperienceMultiplier = n
		case "GAMEPLAY__CLIENTDIRECTORY":
			c.Gameplay.ClientDirectory = value
		case "GAMEPLAY__LAUNCHER":
			c.Gameplay.Launcher = value
		case "GAMEPLAY__BATTLEPASSSTARTSAT", "GAMEPLAY__BATTLEPASSENDSAT":
			stamp, e := time.Parse(time.RFC3339, value)
			if e != nil {
				return c, fmt.Errorf("%s: %w", name, e)
			}
			if key == "GAMEPLAY__BATTLEPASSSTARTSAT" {
				c.Gameplay.BattlePassStartsAt = &stamp
			} else {
				c.Gameplay.BattlePassEndsAt = &stamp
			}
		case "RESOURCES__PVF":
			c.Resources.Pvf = value
		case "RESOURCES__CLIENTPROFILE":
			c.Resources.ClientProfile = value
		case "RESOURCES__PVFSHA256":
			c.Resources.PvfSha256 = value
		case "RESOURCES__PROBE":
			c.Resources.Probe = value
		case "DIAGNOSTICS__FRAMES":
			c.Diagnostics.Frames = value
		case "DIAGNOSTICS__FRAMEMODE":
			c.Diagnostics.FrameMode = value
		case "STORAGE__DATABASE":
			c.Storage.Database = value
		case "VERIFICATION__EVIDENCEROOT":
			c.Verification.EvidenceRoot = value
		case "TESTMODULES__COUNTERENABLED", "TESTMODULES__SHAREDBOUNDARYENABLED":
			v, e := strconv.ParseBool(value)
			if e != nil {
				return c, e
			}
			if key == "TESTMODULES__COUNTERENABLED" {
				c.TestModules.CounterEnabled = v
			} else {
				c.TestModules.SharedBoundaryEnabled = v
			}
		case "LIFECYCLE__STOPFILE":
			c.Lifecycle.StopFile = value
		default:
			if index, ok := strings.CutPrefix(key, "GAMEPLAY__GMACCOUNTS__"); ok {
				n, e := strconv.Atoi(index)
				if e != nil || n < 0 {
					return c, fmt.Errorf("invalid GM account index %q", index)
				}
				// Membership is ordinal. Existing JSON indices are overridden by the environment.
				if n < gmBaseCount {
					c.Gameplay.GmAccounts[n] = value
				} else {
					c.Gameplay.GmAccounts = append(c.Gameplay.GmAccounts, value)
				}
			}
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
	if c.Gameplay.ExperienceMultiplier < 1 || c.Gameplay.ExperienceMultiplier > 100 {
		return c, errors.New("Gameplay:ExperienceMultiplier must be an integer from 1 to 100")
	}
	return c, nil
}

func (c Config) Path(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(c.Root, filepath.FromSlash(path))
}

// Packaged servers keep the prepared catalogs beside their other data.
// Development instances may select a shared project-local export explicitly.
func (c Config) CatalogPath() string {
	if path := os.Getenv("S9A1_CREATION_CATALOG"); path != "" {
		return c.Path(path)
	}
	return c.Path(filepath.Join("Data", "Pvf", "characters.json"))
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
	if c.Gameplay.NativePartyNotices != nil && !*c.Gameplay.NativePartyNotices ||
		c.TestModules.CounterEnabled || c.TestModules.SharedBoundaryEnabled {
		return nil, errors.New("formal local entry requires native party notices and separate test modules")
	}
	if ip := net.ParseIP(s.Address); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("uncredentialed local entry must bind loopback")
	}
	if s.MaximumConnections < 1 || s.MaximumConnections > 128 || s.MaximumFrameBytes < 16 || s.MaximumFrameBytes > 1<<20 {
		return nil, errors.New("invalid transport limits")
	}
	if s.ReceiveChunkBytes < 1 || s.ReceiveChunkBytes > 65536 || s.ReceiveQueueCapacity < 1 || s.ReceiveQueueCapacity > 1024 ||
		s.SendQueueCapacity < 1 || s.SendQueueCapacity > 1024 || s.SessionQueueCapacity < 1 || s.SessionQueueCapacity > 1024 {
		return nil, errors.New("invalid bounded transport capacity")
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
