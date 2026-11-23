package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Permission 是一位权限：read 覆盖浏览、下载与分享，fav 覆盖收藏。
//
// 权限用位表示，一个用户可以同时拥有多位；YAML 里仍然写成 ["read", "fav"] 这样的数组，
// 与 SeaweedFS 的 s3 config 的动作数组一致。
type Permission uint8

const (
	PermissionRead Permission = 1 << iota
	PermissionFav
)

// PermissionNames 是全部权限名，顺序固定：接口返回的权限数组与日志都按它排。
var PermissionNames = []string{"read", "fav"}

var permissionBits = map[string]Permission{
	"read": PermissionRead,
	"fav":  PermissionFav,
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
	// Access 是可见范围的绝对路径列表："/" 表示整个数据目录，"/目录A" 表示只显示该目录。
	// 多值时取并集，并保留各条目的祖先目录（让用户能看到自己真正处在哪一层）。
	// 用户视角的路径始终是数据目录下的完整路径，不做「以某目录为根」的重写。
	Access []string
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
	Access      []string `yaml:"access"`
}

const (
	minUserNameLen = 1
	maxUserNameLen = 64
	minPasswordLen = 8
	maxPasswordLen = 256
)

var userNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// LoadUsersFile 从 YAML 读用户表并校验，任何一个字段不合法都拒绝启动：
// 用户表是登录与可见范围的唯一依据，半可用的用户表比启动失败更难排查。
func LoadUsersFile(filePath string) ([]User, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read users file %s: %w", filePath, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var file usersFile
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse users file %s: %w", filePath, err)
	}
	if err := ensureSingleDocument(decoder); err != nil {
		return nil, fmt.Errorf("parse users file %s: %w", filePath, err)
	}
	if len(file.Users) == 0 {
		return nil, fmt.Errorf("users file %s has no users", filePath)
	}
	seen := make(map[string]bool, len(file.Users))
	users := make([]User, 0, len(file.Users))
	for index, entry := range file.Users {
		user, err := entry.toUser()
		if err != nil {
			return nil, fmt.Errorf("users file %s: users[%d]: %w", filePath, index, err)
		}
		if seen[user.Name] {
			return nil, fmt.Errorf("users file %s: duplicate user name %q", filePath, user.Name)
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
	access, err := normalizeAccess(e.Access)
	if err != nil {
		return User{}, fmt.Errorf("user %q: %w", name, err)
	}
	return User{Name: name, Password: e.Password, Permissions: permissions, Access: access}, nil
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
		return 0, fmt.Errorf("permissions must contain at least one of %s", strings.Join(PermissionNames, ", "))
	}
	// 收藏是浏览之上的附加动作（界面从文件列表进入），没有 read 的 fav 不可用，加载时直接拒绝。
	if permissions.Has(PermissionFav) && !permissions.Has(PermissionRead) {
		return 0, errors.New("fav requires read; add read to permissions")
	}
	return permissions, nil
}

// normalizeAccess 校验并归一化可见范围列表：每项都必须以 "/" 开头。
// 可见范围是访问边界，配错就该拒绝启动；规范化后重复的项、以及 "/" 与其它项混用都直接报错。
func normalizeAccess(items []string) ([]string, error) {
	if len(items) == 0 {
		return nil, errors.New(`access is required (e.g. ["/"] or ["/目录A"])`)
	}
	seen := make(map[string]bool, len(items))
	access := make([]string, 0, len(items))
	for index, raw := range items {
		item, err := normalizeAccessItem(raw)
		if err != nil {
			return nil, fmt.Errorf("access[%d]: %w", index, err)
		}
		if seen[item] {
			return nil, fmt.Errorf("access[%d]: duplicate entry %q", index, item)
		}
		seen[item] = true
		access = append(access, item)
	}
	if seen["/"] && len(access) > 1 {
		return nil, errors.New(`access ["/"] already covers the whole data directory; remove other entries`)
	}
	return access, nil
}

// normalizeAccessItem 校验并归一化单条可见范围：必须以 "/" 开头，"/" 表示整个数据目录。
// 拒绝 ".."、反斜杠与 NUL。允许尾部斜杠（"/共享/" 与 "/共享" 等价）。
func normalizeAccessItem(raw string) (string, error) {
	if raw == "" {
		return "", errors.New(`root is required ("/" for the whole data directory)`)
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("root %q must start with /", raw)
	}
	if strings.ContainsRune(raw, '\\') || strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("root %q contains an illegal character", raw)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(raw, "/"), "/") {
		switch {
		case segment == "":
			// 尾部斜杠与重复斜杠交给 path.Clean；这里只拒绝真正有歧义的段。
		case segment == "." || segment == "..":
			return "", fmt.Errorf("root %q must not contain %q", raw, segment)
		}
	}
	return path.Clean(raw), nil
}

// Visible 报告全局相对路径是否在用户的可见范围内：命中某条 access 的自身、其后代，
// 或它本身就是某条 access 的祖先（祖先链要保留，用户才知道自己处在哪一层）。
//
// 比较大小写敏感：/DOCS 与 /Docs 是不同的路径，配置该怎么写就怎么匹配。
func (u User) Visible(rel string) bool {
	for _, access := range u.Access {
		if access == "/" {
			return true
		}
		item := strings.TrimPrefix(access, "/")
		if rel == item || strings.HasPrefix(rel, item+"/") {
			return true
		}
		if strings.HasPrefix(item, rel+"/") {
			return true
		}
	}
	return false
}
