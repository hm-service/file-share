package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Permission 是一位权限：read 覆盖浏览与下载这类只读操作，write 覆盖上传与改动布局。
//
// 权限用位表示，一个用户可以同时拥有多位；YAML 里仍然写成 ["read", "write"] 这样的数组，
// 与 SeaweedFS 的 s3 config 的动作数组一致。
type Permission uint8

const (
	PermissionRead Permission = 1 << iota
	PermissionWrite
)

// PermissionNames 是全部权限名，顺序固定：接口返回的权限数组与日志都按它排。
var PermissionNames = []string{"read", "write"}

var permissionBits = map[string]Permission{
	"read":  PermissionRead,
	"write": PermissionWrite,
}

// Has 判断是否包含 required（required 可以是多位，必须全部满足）。
func (p Permission) Has(required Permission) bool { return p&required == required }

// Names 返回权限名数组，给接口 JSON 用。
func (p Permission) Names() []string {
	names := make([]string, 0, len(PermissionNames))
	for _, name := range PermissionNames {
		if p.Has(permissionBits[name]) {
			names = append(names, name)
		}
	}
	return names
}

// User 是用户表里的一项。运行期只读：改用户表要重启进程。
type User struct {
	Name        string
	Password    string
	Permissions Permission
}

// Can 报告该用户是否拥有 required 权限。
func (u User) Can(required Permission) bool { return u.Permissions.Has(required) }

// usersFile 是 YAML 的顶层结构：与 s3 config 一样是「身份列表」。
type usersFile struct {
	Users []userEntry `yaml:"users"`
}

type userEntry struct {
	Name        string   `yaml:"name"`
	Password    string   `yaml:"password"`
	Permissions []string `yaml:"permissions"`
}

const (
	minUserNameLen = 1
	maxUserNameLen = 64
	minPasswordLen = 8
	maxPasswordLen = 256
)

var userNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// LoadUsersFile 从 YAML 读用户表并校验，任何一个字段不合法都拒绝启动：
// 用户表是登录的唯一依据，半可用的用户表比启动失败更难排查。
func LoadUsersFile(path string) ([]User, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users file %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var file usersFile
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse users file %s: %w", path, err)
	}
	if err := ensureSingleDocument(decoder); err != nil {
		return nil, fmt.Errorf("parse users file %s: %w", path, err)
	}
	if len(file.Users) == 0 {
		return nil, fmt.Errorf("users file %s has no users", path)
	}
	seen := make(map[string]bool, len(file.Users))
	users := make([]User, 0, len(file.Users))
	for index, entry := range file.Users {
		user, err := entry.toUser()
		if err != nil {
			return nil, fmt.Errorf("users file %s: users[%d]: %w", path, index, err)
		}
		if seen[user.Name] {
			return nil, fmt.Errorf("users file %s: duplicate user name %q", path, user.Name)
		}
		seen[user.Name] = true
		users = append(users, user)
	}
	return users, nil
}

// ensureSingleDocument 拒绝 YAML 里出现第二份文档：多份文档只会让读到的用户表被悄悄忽略一半。
func ensureSingleDocument(decoder *yaml.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("users file must contain a single document")
}

func (e userEntry) toUser() (User, error) {
	name := strings.TrimSpace(e.Name)
	if !userNamePattern.MatchString(name) || len(name) < minUserNameLen || len(name) > maxUserNameLen {
		return User{}, fmt.Errorf("invalid name %q (need 1-%d chars of A-Za-z0-9._- starting with a letter or digit)",
			e.Name, maxUserNameLen)
	}
	if len(e.Password) < minPasswordLen || len(e.Password) > maxPasswordLen {
		return User{}, fmt.Errorf("user %q: password length must be %d-%d", name, minPasswordLen, maxPasswordLen)
	}
	permissions, err := parsePermissions(e.Permissions)
	if err != nil {
		return User{}, fmt.Errorf("user %q: %w", name, err)
	}
	return User{Name: name, Password: e.Password, Permissions: permissions}, nil
}

// parsePermissions 把 YAML 里的权限名数组换成位集合，未知权限名直接报错。
func parsePermissions(names []string) (Permission, error) {
	var permissions Permission
	for _, name := range names {
		bit, ok := permissionBits[strings.TrimSpace(name)]
		if !ok {
			return 0, fmt.Errorf("unknown permission %q (allowed: %s)", name, strings.Join(PermissionNames, ", "))
		}
		permissions |= bit
	}
	if permissions == 0 {
		return 0, errors.New("permissions must contain at least one of read, write")
	}
	// 只读浏览是写操作的前提（界面与 /api/files、/api/me 都要求 read），
	// 只配 write 的账号登录后等于不可用，直接在加载时拒绝。
	if permissions.Has(PermissionWrite) && !permissions.Has(PermissionRead) {
		return 0, errors.New("write requires read; add read to permissions")
	}
	return permissions, nil
}
