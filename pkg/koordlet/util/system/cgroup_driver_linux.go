//go:build linux
// +build linux

package system

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

const (
	kubeletConfigCgroupDriverKey = "cgroupDriver"
)

func GuessCgroupDriverFromCgroupName() CgroupDriverType {
	systemdKubepodDirExists := FileExists(filepath.Join(Conf.CgroupRootDir, "cpu", KubeRootNameSystemd))
	cgroupfsKubepodDirExists := FileExists(filepath.Join(Conf.CgroupRootDir, "cpu", KubeRootNameCgroupfs))
	if systemdKubepodDirExists != cgroupfsKubepodDirExists {
		if systemdKubepodDirExists {
			return Systemd
		} else {
			return Cgroupfs
		}
	}
	return ""
}

// Guess Kubelet's cgroup driver from command line args.
// 1. Read kubelet's /proc/${pid}/cmdline.
// 2. If '--cgroup-driver' in args, that's it.
//    else if '--config' not in args, is default driver('cgroupfs').
//    else go to step-3.
// 3. If kubelet config is relative path, join with /proc/${pidof kubelet}/cwd.
//    search 'cgroupDriver:' in kubelet config file, that's it.
func GuessCgroupDriverFromKubelet() (CgroupDriverType, error) {
	pids, err := PidOf(Conf.ProcRootDir, "kubelet")
	if err != nil || len(pids) == 0 {
		return "", fmt.Errorf("failed to find kubelet's pid, kubelet may stop: %v", err)
	}
	kubeletPid := pids[0]

	kubeletArgs, err := ProcCmdLine(Conf.ProcRootDir, kubeletPid)
	if err != nil || len(kubeletArgs) <= 1 {
		return "", fmt.Errorf("failed to get kubelet's args: %v", err)
	}

	var argsCgroupDriver string
	var argsConfigFile string
	fs := pflag.NewFlagSet("GuessTest", pflag.ContinueOnError)
	fs.ParseErrorsWhitelist.UnknownFlags = true
	fs.StringVar(&argsCgroupDriver, "cgroup-driver", "", "")
	fs.StringVar(&argsConfigFile, "config", "", "")
	if err := fs.Parse(kubeletArgs[1:]); err != nil {
		return "", fmt.Errorf("failed to parse kubelet's args, kubelet version may not support: %v", err)
	}
	// kubelet command-line args will override configuration from config file
	if argsCgroupDriver != "" {
		return CgroupDriverType(argsCgroupDriver), nil
	} else if argsConfigFile == "" {
		klog.Infof("Neither '--cgroup-driver' or '--config' is specify, use default: '%s'", string(kubeletDefaultCgroupDriver))
		return kubeletDefaultCgroupDriver, nil
	}

	// parse kubelet config file
	var kubeletConfigFile string
	if filepath.IsAbs(argsConfigFile) {
		kubeletConfigFile = argsConfigFile
	} else {
		kubletCWD, err := os.Readlink(filepath.Join(Conf.ProcRootDir, strconv.Itoa(kubeletPid), "cwd"))
		if err != nil {
			klog.Errorf("failed to get kubelet's cwd: %v", err)
			if exePath, err := os.Readlink(filepath.Join(Conf.ProcRootDir, strconv.Itoa(kubeletPid), "exe")); err != nil {
				kubletCWD = filepath.Dir(exePath)
			} else {
				kubletCWD = "/"
			}
		}
		kubeletConfigFile = filepath.Join(kubletCWD, argsConfigFile)
	}

	// kubelet config file is in host path
	fileBuf, _, err := ExecCmdOnHost([]string{"cat", kubeletConfigFile})
	if err != nil {
		return "", fmt.Errorf("failed to read kubelet's config file(%s): %v", kubeletConfigFile, err)
	}
	scanner := bufio.NewScanner(bytes.NewBuffer(fileBuf))
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		parts := strings.Fields(line)
		// remove trailing ':' from key
		key := parts[0][:len(parts[0])-1]
		if key == kubeletConfigCgroupDriverKey {
			return CgroupDriverType(strings.TrimSpace(parts[1])), nil
		}
	}
	klog.Infof("Cgroup driver is not specify in kubelet config file, use default: '%s'", kubeletDefaultCgroupDriver)
	return kubeletDefaultCgroupDriver, nil
}