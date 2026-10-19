package youtube

import "testing"

func TestParseDevices(t *testing.T) {
	out := "List of detected audio devices:\n  'auto' (Autoselect device)\n  'wasapi/{1a7e}' (Динамики (Realtek(R) Audio))\n  'wasapi/{4972}' (CABLE Input (VB-Audio Virtual Cable))\n  'openal' (Default (openal))\n"
	got := parseDevices(out)
	if len(got) != 2 {
		t.Fatalf("ждали два устройства, получили %v", got)
	}
	if got[0].ID != "wasapi/{1a7e}" || got[0].Name != "Динамики (Realtek(R) Audio)" {
		t.Errorf("первое устройство разобрано неверно: %+v", got[0])
	}
	if got[1].Name != "CABLE Input (VB-Audio Virtual Cable)" {
		t.Errorf("второе устройство разобрано неверно: %+v", got[1])
	}
}
