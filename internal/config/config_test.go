package config

import "testing"

// 古い config.yaml（標準より少ない拡張子）でも、標準拡張子が常に補完されることを検証する。
// 例: extensions.video に mp4/m4v しか書いていなくても avi/wmv/mkv/flv が認識される。
func TestApplyDefaults_UnionExtensions(t *testing.T) {
	c := &Config{}
	c.Extensions.Video = []string{"mp4", "m4v"}
	c.Extensions.Image = []string{"jpg"}
	c.applyDefaults()

	wantVideo := []string{"avi", "wmv", "mkv", "flv", "mp4", "m4v", "webm", "mov"}
	for _, ext := range wantVideo {
		if !contains(c.Extensions.Video, ext) {
			t.Errorf("video 拡張子に %q が含まれていません: %v", ext, c.Extensions.Video)
		}
	}
	if !contains(c.Extensions.Image, "jpg") || !contains(c.Extensions.Image, "png") {
		t.Errorf("image 拡張子の補完に失敗: %v", c.Extensions.Image)
	}
}

// 空設定でも標準拡張子がすべて入ること。
func TestApplyDefaults_Empty(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if len(c.Extensions.Video) != len(defaultVideoExts) {
		t.Errorf("空設定の video 拡張子数が不正: got %v", c.Extensions.Video)
	}
	if len(c.Extensions.Image) != len(defaultImageExts) {
		t.Errorf("空設定の image 拡張子数が不正: got %v", c.Extensions.Image)
	}
}

// 正規化（大文字・先頭ドット）と重複排除を検証する。
func TestUnionExts_NormalizeAndDedup(t *testing.T) {
	got := unionExts([]string{"mp4", "MOV"}, []string{".MP4", "mkv", " avi "})
	want := []string{"mp4", "mov", "mkv", "avi"}
	if len(got) != len(want) {
		t.Fatalf("件数が不正: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("位置 %d: got %q want %q (%v)", i, got[i], want[i], got)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
