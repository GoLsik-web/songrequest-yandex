package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixMojibake(t *testing.T) {
	cases := map[string]string{
		"Р—Р°РєР°Р· С‚СЂРµРєР°": "Заказ трека",
		"Заказ трека":                 "Заказ трека", // настоящий текст не трогаем
		"Song request":                "Song request",
		"Привет, мир! ёЁ":             "Привет, мир! ёЁ",
		"★ Заказ ★":                   "★ Заказ ★",
	}
	for in, want := range cases {
		if got, _ := fixMojibake(in); got != want {
			t.Errorf("%q: ждали %q, получили %q", in, want, got)
		}
	}
}

// Файл, испорченный правкой «руками», чинится при запуске и сразу
// записывается обратно уже правильным. С BOM в начале — тоже.
func TestLoadRepairsMojibakeConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := "\xef\xbb\xbf" + `{"reward_title": "Р—Р°РєР°Р· С‚СЂРµРєР°", "reward_cost": 500}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.Broken != "" {
		t.Fatal("файл с BOM не должен считаться битым")
	}
	if got := f.Get(); got.RewardTitle != "Заказ трека" || got.RewardCost != 500 {
		t.Fatalf("ждали починенное название и цену 500, получили %q %d", got.RewardTitle, got.RewardCost)
	}
	again, _ := Load(dir)
	if again.Get().RewardTitle != "Заказ трека" {
		t.Fatal("починка не записалась на диск")
	}
}
