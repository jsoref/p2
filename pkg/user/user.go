package user

import (
	"bytes"
	"errors"
	"os/exec"
	osuser "os/user"
	"runtime"
	"strconv"

	"github.com/square/p2/pkg/util"
)

var AlreadyExists error
var NoAddFacility error

func init() {
	AlreadyExists = errors.New("The user already exists")
	NoAddFacility = errors.New("Cannot add users on this OS.")
}

func CreateUser(username string, homedir string) (*osuser.User, error) {
	if runtime.GOOS != "linux" {
		return nil, NoAddFacility
	}
	user, err := osuser.Lookup(username)
	if err == nil {
		return user, AlreadyExists
	}
	add := exec.Command("adduser", "-d", homedir, username)
	errout := bytes.Buffer{}
	add.Stderr = &errout
	err = add.Run()
	if err != nil {
		return nil, util.Errorf("Couldn't add new user %s: %s: %s", username, err, errout.String())
	}
	return osuser.Lookup(username)
}

func IDs(username string) (int, int, error) {
	user, err := osuser.Lookup(username)
	if err != nil {
		return 0, 0, util.Errorf(err.Error())

	}
	uid, err := strconv.ParseInt(user.Uid, 10, 0)
	if err != nil {
		return 0, 0, util.Errorf(err.Error())
	}
	gid, err := strconv.ParseInt(user.Gid, 10, 0)
	if err != nil {
		return 0, 0, util.Errorf(err.Error())

	}
	return int(uid), int(gid), nil
}
