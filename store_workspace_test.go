package main

import "testing"

// RepointWorkspace：默认工作区改名迁移时把显式挂在旧目录上的会话改指新目录；
// 空路径不动作；大小写不同的同一路径也算命中；未设工作目录（空串）与用户自选
// 目录都不动。
func TestRepointWorkspace(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer s.db.Close()
	oldWS := `C:\Users\x\AppData\Local\Programs\坚祥数字助理GO\工作区`
	newWS := `C:\Users\x\AppData\Local\Programs\坚祥数字助理GO\默认工作区`
	for id, ws := range map[string]string{
		"s-old":          oldWS,
		"s-old-diffcase": `c:\users\x\appdata\local\programs\坚祥数字助理GO\工作区`,
		"s-default":      "",
		"s-other":        `D:\项目`,
	} {
		m := &SessionMeta{ID: id, Title: id, Workspace: ws, CreatedAt: now(), UpdatedAt: now()}
		if err := s.Create(m); err != nil {
			t.Fatalf("建会话 %s 失败: %v", id, err)
		}
	}

	n, err := s.RepointWorkspace(oldWS, newWS)
	if err != nil {
		t.Fatalf("改指失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应改指 2 个会话，得到 %d", n)
	}
	for _, tc := range []struct{ id, want string }{
		{"s-old", newWS},
		{"s-old-diffcase", newWS},
		{"s-default", ""},
		{"s-other", `D:\项目`},
	} {
		m, err := s.Get(tc.id)
		if err != nil || m == nil {
			t.Fatalf("读会话 %s 失败: %v", tc.id, err)
		}
		if m.Workspace != tc.want {
			t.Fatalf("会话 %s 的工作目录应为 %q，得到 %q", tc.id, tc.want, m.Workspace)
		}
	}

	if n, err := s.RepointWorkspace("", newWS); err != nil || n != 0 {
		t.Fatalf("空旧路径不应动作: n=%d err=%v", n, err)
	}
	if n, err := s.RepointWorkspace(oldWS, ""); err != nil || n != 0 {
		t.Fatalf("空新路径不应动作: n=%d err=%v", n, err)
	}
}
