//go:build linux

package syncthing

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type groupMember struct {
	PID   int
	Group int
	State byte
}

func DetectForeignConflict() (Conflict, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return Conflict{}, err
	}
	conflict := Conflict{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		member, err := readProcStat(pid)
		if err != nil || member.State == 'Z' {
			continue
		}
		command, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		cmdline, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		firstArgument := strings.SplitN(string(cmdline), "\x00", 2)[0]
		if strings.HasPrefix(strings.TrimSpace(string(command)), "syncthing") || strings.HasPrefix(filepath.Base(firstArgument), "syncthing") {
			conflict.ProcessIDs = append(conflict.ProcessIDs, pid)
		}
	}
	sort.Ints(conflict.ProcessIDs)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		bound, err := procNetHasListener(path, 8384)
		if err != nil && !os.IsNotExist(err) {
			return Conflict{}, err
		}
		conflict.ConventionalPort = conflict.ConventionalPort || bound
	}
	return conflict, nil
}

func currentProcessGroup() (int, error) {
	return unix.Getpgrp(), nil
}

func processGroup(pid int) (int, error) {
	return unix.Getpgid(pid)
}

func signalGroupMembers(groupID, excludePID int, signal syscall.Signal) error {
	members, err := listGroupMembers(groupID, excludePID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if err := unix.Kill(member.PID, signal); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("signal group member %d: %w", member.PID, err)
		}
	}
	return nil
}

func groupAbsent(groupID, excludePID int) bool {
	members, err := listGroupMembers(groupID, excludePID)
	return err == nil && len(members) == 0
}

// describeGroup renders a one-line census of the surviving reserved group for
// the stop phase log. Naming the survivors is what separates "upstream is slow
// to exit" from "a monitor grandchild outlived it" after the fact.
func describeGroup(groupID, excludePID int) string {
	members, err := listGroupMembers(groupID, excludePID)
	if err != nil {
		return "group census unavailable: " + err.Error()
	}
	if len(members) == 0 {
		return "group empty"
	}
	descriptions := make([]string, 0, len(members))
	for _, member := range members {
		command, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(member.PID), "comm"))
		name := strings.TrimSpace(string(command))
		if name == "" {
			name = "?"
		}
		descriptions = append(descriptions,
			fmt.Sprintf("%d/%s/%c", member.PID, name, member.State))
	}
	return "group survivors: " + strings.Join(descriptions, " ")
}

func waitForGroupAbsence(ctx context.Context, groupID, excludePID int) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if groupAbsent(groupID, excludePID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("verify upstream group absence: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func listGroupMembers(groupID, excludePID int) ([]groupMember, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	members := make([]groupMember, 0, 4)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == excludePID {
			continue
		}
		member, err := readProcStat(pid)
		if err != nil || member.Group != groupID || member.State == 'Z' {
			continue
		}
		members = append(members, member)
	}
	return members, nil
}

func readProcStat(pid int) (groupMember, error) {
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return groupMember{}, err
	}
	closing := strings.LastIndexByte(string(contents), ')')
	if closing < 0 || closing+2 >= len(contents) {
		return groupMember{}, errors.New("malformed proc stat")
	}
	fields := strings.Fields(string(contents[closing+2:]))
	if len(fields) < 3 || len(fields[0]) != 1 {
		return groupMember{}, errors.New("short proc stat")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return groupMember{}, err
	}
	return groupMember{PID: pid, Group: group, State: fields[0][0]}, nil
}

func procNetHasListener(path string, port int) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	wantPort := strings.ToUpper(fmt.Sprintf("%04X", port))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		parts := strings.Split(fields[1], ":")
		if len(parts) == 2 && parts[1] == wantPort {
			return true, nil
		}
	}
	return false, scanner.Err()
}
