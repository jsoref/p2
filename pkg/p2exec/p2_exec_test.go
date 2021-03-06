package p2exec

import (
	"strings"
	"testing"
)

func TestBuildWithArgs(t *testing.T) {
	args := P2ExecArgs{
		Command: []string{"script"},
	}

	expected := "script"
	actual := strings.Join(args.CommandLine(), " ")
	if actual != expected {
		t.Errorf("Expected args.BuildWithArgs() to return '%s', was '%s'", expected, actual)
	}

	args = P2ExecArgs{
		Command:          []string{"script"},
		NoLimits:         true,
		User:             "some_user",
		EnvDirs:          []string{"some_dir", "other_dir"},
		CgroupConfigName: "some_cgroup_config_name",
		CgroupName:       "cgroup_name",
	}

	expected = "-n -u some_user -e some_dir -e other_dir -l some_cgroup_config_name -c cgroup_name script"
	actual = strings.Join(args.CommandLine(), " ")
	if actual != expected {
		t.Errorf("Expected args.BuildWithArgs() to return '%s', was '%s'", expected, actual)
	}
}
