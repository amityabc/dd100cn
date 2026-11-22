package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"s9a1/server/internal/config"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"slices"
	"strings"
)

const evidenceManifestHash = "3E4EAC61A2B99FF50DDD435C972A66EA0EE30EAE8619660617708CD285B0962D"
const profileHash = "DF01E9A78304C92F9F00A6B8C596F3EBADD4F5C5B20D2051A6A5EC019B25305A"
const channelHash = "9396598201BE4FD8479E16432CBF136A2529B5DA2785ABE6B3BF6D3359722793"

type inputFileCheck struct {
	Name, Configuration    string
	Path                   *string
	Required               bool
	IdentityBasis, Outcome string
	Bytes                  *int64
	Sha256                 *string
	ExpectedSha256         []string
	Error                  *string
}
type inputCheck struct {
	Name, Outcome string
	Error         *string
}
type inputPreflightReport struct {
	Schema                       int
	Outcome                      string
	Passed                       bool
	BaselineCommit, Policy, Root string
	EvidenceRoot                 *string
	Files                        []inputFileCheck
	Checks                       []inputCheck
	Pending                      []string
}

func inputFile(name, key, path string, required bool, expected ...string) inputFileCheck {
	r := inputFileCheck{Name: name, Configuration: key, Required: required, IdentityBasis: "CurrentFingerprintOnly",
		ExpectedSha256: append([]string{}, expected...)}
	if strings.HasPrefix(name, "Evidence") {
		r.IdentityBasis = "AnchoredOriginalFixtureManifest"
	} else if len(expected) > 0 {
		r.IdentityBasis = "Accepted7fInputOrFixedLauncherSource"
	}
	if path == "" {
		r.Outcome = "OptionalAbsent"
		if required {
			r.Outcome = "MissingConfiguration"
		}
		return r
	}
	r.Path = &path
	h, n, err := fileFingerprint(path)
	if errors.Is(err, os.ErrNotExist) {
		r.Outcome = "OptionalAbsent"
		if required {
			r.Outcome = "MissingFile"
		}
	} else if err != nil {
		msg := err.Error()
		r.Outcome, r.Error = "ReadFailed", &msg
	} else {
		r.Bytes, r.Sha256 = &n, &h
		switch {
		case n == 0:
			r.Outcome = "EmptyFile"
		case len(expected) == 0:
			r.Outcome = "PresentFingerprinted"
		case slices.Contains(expected, h):
			r.Outcome = "Matched"
		default:
			r.Outcome = "VersionMismatch"
		}
	}
	return r
}

func (r *inputPreflightReport) check(name string, err error) {
	item := inputCheck{Name: name, Outcome: "Passed"}
	if err != nil {
		msg := err.Error()
		item.Outcome, item.Error = "Failed", &msg
	}
	r.Checks = append(r.Checks, item)
}

func (r *inputPreflightReport) evidence(root string) error {
	path := ""
	if root != "" {
		path = filepath.Join(root, "fixture-manifest.json")
	}
	m := inputFile("EvidenceManifest", "Verification:EvidenceRoot/fixture-manifest.json", path, true, evidenceManifestHash)
	r.Files = append(r.Files, m)
	if m.Outcome != "Matched" {
		return errors.New("explicit anchored 33-file evidence is missing or does not match; no synthetic fallback")
	}
	raw, err := boundedRead(path, 64*1024)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%X", sha256.Sum256(raw)) != evidenceManifestHash {
		return errors.New("evidence manifest changed during precheck")
	}
	var rows []struct {
		Path, Sha256 string
		Bytes        int64
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 32 {
		return errors.New("evidence manifest must contain 32 original entries")
	}
	names := map[string]bool{}
	passed := true
	for _, e := range rows {
		name := strings.ToLower(e.Path)
		if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:") || names[name] ||
			!completeHex(e.Sha256, 64) || e.Bytes < 1 {
			return errors.New("invalid original fixture identity")
		}
		names[name] = true
		f := inputFile("Evidence:"+e.Path, "Verification:EvidenceRoot", filepath.Join(root, e.Path), true, e.Sha256)
		if f.Outcome == "Matched" && *f.Bytes != e.Bytes {
			f.Outcome = "LengthMismatch"
		}
		passed = passed && f.Outcome == "Matched"
		r.Files = append(r.Files, f)
	}
	if !passed {
		return errors.New("one or more original fixture files differ")
	}
	return nil
}

func startupPvfCheck(c config.Config) error {
	expected := strings.TrimSpace(c.Resources.PvfSha256)
	if expected != "" && !completeHex(expected, 64) {
		return errors.New("Resources:PvfSha256 must be a complete SHA256")
	}
	hash, n, err := fileFingerprint(c.Path(c.Resources.Pvf))
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("required Script.pvf is empty")
	}
	if expected != "" && !strings.EqualFold(expected, hash) {
		return errors.New("server PVF SHA256 differs from Resources:PvfSha256")
	}
	if client := settingPath(c, c.Gameplay.ClientDirectory); client != "" {
		h, b, err := fileFingerprint(filepath.Join(client, "Script.pvf"))
		if err != nil {
			return err
		}
		if h != hash || b != n {
			return errors.New("client and server Script.pvf size/SHA256 differ")
		}
	}
	return nil
}

func preflight(c config.Config, evidence string) inputPreflightReport {
	r := inputPreflightReport{Schema: 1, Root: c.Root, BaselineCommit: "7f33f79ceec144909f5a5f4a3bab8885ea090964", Pending: []string{},
		Policy: "Passed means required files exist, historically pinned inputs and original evidence match, the selected launcher passes its legacy or paired source identity, and readonly Go resource preparation succeeds. No listening, database, launcher, bridge or resource writes."}
	if evidence == "" {
		evidence = c.Verification.EvidenceRoot
	}
	evidence = settingPath(c, evidence)
	if evidence != "" {
		r.EvidenceRoot = &evidence
	}
	clientDir := settingPath(c, c.Gameplay.ClientDirectory)
	clientFile := func(file string) string {
		if clientDir == "" {
			return ""
		}
		return filepath.Join(clientDir, filepath.FromSlash(file))
	}
	add := func(name, key, path string, required bool, expected ...string) inputFileCheck {
		f := inputFile(name, key, path, required, expected...)
		r.Files = append(r.Files, f)
		if f.Outcome == "PresentFingerprinted" {
			r.Pending = append(r.Pending, name+": historical byte identity is unconfirmed; this check establishes presence and the current fingerprint only")
		}
		return f
	}
	add("Client", "Gameplay:ClientDirectory/DNF.exe", clientFile("DNF.exe"), true, "C1B772ACB7C4C5086A4AD64DA619DEA3547B73BFD2D06DB93A24367186DD0E8B")
	add("ClientBase", "Gameplay:ClientDirectory/DNFBase.dll", clientFile("DNFBase.dll"), true, "5A5CA2ED784EE7D6351DAECCFBC8DF91D0D8D58842AECF6B3CA6C7F335F5BBEA")
	add("ClientProxy", "Gameplay:ClientDirectory/TenProxy.dll", clientFile("TenProxy.dll"), true, "8E6C978EE255996FCBBE235EDFD502DEB5BAF3D5210A3130AEF98BA3D66E0FA3")
	launcher := settingPath(c, c.Gameplay.Launcher)
	identity, err := validateLauncher(clientDir, launcher)
	hash := identity.Hash
	if hash == "" {
		hash = matchingLauncherSHA256
	}
	add("Launcher", "Gameplay:Launcher", launcher, true, hash)
	if identity.Source && err == nil {
		r.Files[len(r.Files)-1].IdentityBasis = "VerifiedSourceBuildManifest"
	}
	r.check("ClientLauncherIdentity", err)
	for _, name := range []string{"Script.pvf", "sk.dat", "dstr.dat", "D3DX9_43.dll", "fmodex64.dll"} {
		add("ClientCompanion:"+name, "Gameplay:ClientDirectory/"+name, clientFile(name), true)
	}
	const background = "AntiCheatExpert/InGame/x64/ACE-PBC-Game64.dll"
	add("ClientBackground", "Gameplay:ClientDirectory/"+background, clientFile(background), false,
		"712A9CDB6C7F15385F131A66B6C20994EE0898020B59D500324A87DD1036E251", "7DA025E622F83A2646293C53FC211EDAE63A59C7A46BDE2FF332EFBA631FDD85")
	pvf := add("ServerPvf", "Resources:Pvf", settingPath(c, c.Resources.Pvf), true)
	profile := add("ClientProfile", "Resources:ClientProfile", settingPath(c, c.Resources.ClientProfile), true, profileHash)
	channel := add("ChannelScript", "Gameplay:ChannelScript", settingPath(c, c.Gameplay.ChannelScript), true, channelHash)
	r.check("PvfStartupIntegrity", startupPvfCheck(c))
	r.check("CompleteOriginalEvidence", r.evidence(evidence))
	r.check("LaunchConfiguration", preflightLaunchConfig(c))
	if pvf.Outcome == "PresentFingerprinted" && profile.Outcome == "Matched" && channel.Outcome == "Matched" {
		r.check("ReadonlyResourcePreparation", preflightResources(c))
	} else {
		msg := "requires a present nonempty PVF and independently matched profile and channel script"
		r.Checks = append(r.Checks, inputCheck{"ReadonlyResourcePreparation", "SkippedInputFailure", &msg})
	}
	stable := true
	for _, f := range r.Files {
		if f.Outcome != "Matched" && f.Outcome != "PresentFingerprinted" {
			continue
		}
		f2 := inputFile(f.Name, f.Configuration, *f.Path, f.Required, f.ExpectedSha256...)
		if f.Outcome != f2.Outcome || *f.Bytes != *f2.Bytes || *f.Sha256 != *f2.Sha256 {
			stable = false
			r.check("InputStable:"+f.Name, errors.New("input changed or became unreadable during precheck"))
		}
	}
	var unstable error
	if !stable {
		unstable = errors.New("input changed during precheck")
	}
	r.check("InputsStableAtCompletion", unstable)
	if identity.Source && err == nil {
		after, problem := validateLauncher(clientDir, launcher)
		if problem == nil && identity != after {
			problem = errors.New("selected source build changed during precheck")
		}
		r.check("ClientSourcePackageStableAtCompletion", problem)
	}
	if evidence == "" {
		r.Pending = append(r.Pending, "Complete original replay evidence is missing; public or synthetic fixtures cannot authorize full acceptance")
	}
	r.Pending = append(r.Pending, "Client companion fingerprints establish current presence only; full client package compatibility and client UI are not observed by this precheck")
	r.Passed = true
	for _, f := range r.Files {
		r.Passed = r.Passed && (f.Outcome == "Matched" || f.Outcome == "PresentFingerprinted" || !f.Required && f.Outcome == "OptionalAbsent")
	}
	for _, f := range r.Checks {
		r.Passed = r.Passed && f.Outcome == "Passed"
	}
	r.Outcome = "InputPreflightFailed"
	if r.Passed {
		r.Outcome = "InputsVerified"
	}
	return r
}

func preflightLaunchConfig(c config.Config) error {
	if err := fixedLaunchConfig(c); err != nil {
		return err
	}
	if _, err := c.StopPath(); err != nil {
		return err
	}
	if _, err := frameLogPath(c); err != nil {
		return err
	}
	runtimeDir := filepath.Join(c.Root, "runtime")
	for name, path := range map[string]string{"Storage:Database": c.Storage.Database, "Diagnostics:Frames": c.Diagnostics.Frames} {
		rel, err := filepath.Rel(runtimeDir, c.Path(path))
		if err != nil || !filepath.IsLocal(rel) || rel == "." {
			return fmt.Errorf("%s must be under runtime", name)
		}
	}
	return nil
}

func preflightResources(c config.Config) error {
	b, err := os.ReadFile(c.Path(c.Gameplay.ChannelScript))
	if err != nil {
		return err
	}
	if _, err = protocol.ParseChannelCatalog(b, true); err != nil {
		return err
	}
	_, err = resources.LoadCharacters(runtimeCatalogPath(c, false), c.Path(c.Resources.Pvf), c.Path(c.Resources.ClientProfile), c.Gameplay.ExperienceMultiplier)
	return err
}
