package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"

	"s9a1/server/internal/multiplayer"
)

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("launcher", flag.ContinueOnError)
	profile := flags.String("profile", "", "private player credential file")
	client := flags.String("client", "", "unpacked Windows client directory containing its matching Start-Client.ps1")
	bridgeOnly := flags.Bool("bridge-only", false, "run authenticated local listeners; start the matching client separately")
	dotnetRoot := flags.String("dotnet-root", "", "optional .NET 10 runtime directory for the original Windows resource tool")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *profile == "" || flags.NArg() != 0 || (*client == "") != *bridgeOnly {
		return errors.New("supply -profile and either -client <directory> or -bridge-only")
	}
	var command *exec.Cmd
	if !*bridgeOnly {
		if runtime.GOOS != "windows" {
			return errors.New("launching DNF requires Windows")
		}
		dir, err := filepath.Abs(*client)
		if err != nil {
			return err
		}
		for _, name := range []string{"DNF.exe", "Start-Client.ps1", "Resource-Launcher.ps1"} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("invalid client file %s", name)
			}
		}
		shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		command = exec.CommandContext(ctx, shell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(dir, "Start-Client.ps1"), "-ClientRoot", dir)
		command.Dir = dir
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if *dotnetRoot != "" {
			runtimeDir, err := filepath.Abs(*dotnetRoot)
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(runtimeDir, "dotnet.exe")); err != nil {
				return err
			}
			command.Env = append(os.Environ(), "DOTNET_ROOT="+runtimeDir, "DOTNET_ROOT_X64="+runtimeDir)
		}
	}
	player, err := multiplayer.LoadPlayer(*profile)
	if err != nil {
		return err
	}
	proxy, err := multiplayer.StartLocal(ctx, player, func(err error) { fmt.Fprintln(os.Stderr, "Gateway stream:", err) })
	if err != nil {
		return err
	}
	defer proxy.Close()
	fmt.Println("Authenticated gateway ready. Original channel and game listeners are bound on 127.0.0.1.")
	if command != nil {
		if err := command.Run(); err != nil {
			return fmt.Errorf("original client startup: %w", err)
		}
	}
	fmt.Println("Keep this launcher running during play. Press Ctrl+C to close its connections.")
	<-ctx.Done()
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
