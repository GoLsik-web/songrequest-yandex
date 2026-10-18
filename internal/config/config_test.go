package config

import "testing"

// Дефолт режима возврата — «запасной плейлист»: он предсказуемее всего.
// Пока плейлист не выбран, он молча работает как «ничего не включать».
func TestDefaultResumeMode(t *testing.T) {
	d := Defaults()
	if d.ResumeFail != ResumeFallbackPlaylist {
		t.Fatalf("ждали %q, получили %q", ResumeFallbackPlaylist, d.ResumeFail)
	}
	if !d.ResumeModeIncomplete() {
		t.Fatal("без выбранного плейлиста режим должен считаться недонастроенным")
	}
	if d.EffectiveResumeMode() != ResumeNothing {
		t.Fatalf("ждали деградацию до тишины, получили %q", d.EffectiveResumeMode())
	}
}
