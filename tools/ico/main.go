// Команда tools/ico рисует значок приложения в файл internal/desktop/icon.ico.
//
// Зачем отдельная программа. Значок нужен трею возле часов и окну программы, а
// Windows там понимает только формат .ico — SVG из панели (web/favicon.svg) он
// не берёт. Готовых средств рисования .ico в Go нет, а тащить ради одного файла
// внешнюю программу (ImageMagick) значит сломать правило «сборка одной
// командой». Поэтому картинка рисуется руками здесь, а результат лежит в
// репозитории и меняется только когда меняется сам значок:
//
//	go run ./tools/ico
//
// Тот же значок вшит в сам exe (его видно в проводнике и на ярлыке) файлом
// rsrc_windows_amd64.syso в корне. Go подхватывает его при сборке сам. После
// смены значка пересобрать и его:
//
//	go run github.com/tc-hib/go-winres@v0.3.3 simply --icon internal/desktop/icon.ico --arch amd64
//
// Рисунок повторяет web/favicon.svg: жёлтый квадрат «Нефора» с чёрной нотой.
// Прежний значок — лаймовая точка на тёмном — в трее путали со Spotify.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Краски из дизайна «Нефор». Меняются вместе с web/favicon.svg, не по отдельности.
var (
	yellow = rgb{0xff, 0xcc, 0x00}
	ink    = rgb{0x0c, 0x0c, 0x0c}
)

type rgb struct{ R, G, B uint8 }

// sizes — все размеры значка в одном файле. Windows сам берёт подходящий:
// 16 для трея, 32 для окна, 128 для Alt+Tab на крупном экране. Если нужного
// размера в файле нет, система растянет ближайший, и края расползутся.
var sizes = []int{16, 20, 24, 32, 48, 64, 128, 256}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func run() error {
	var images [][]byte
	for _, s := range sizes {
		images = append(images, dib(draw(s), s))
	}

	var buf bytes.Buffer
	// Заголовок ICONDIR: два нуля, тип 1 (значок), сколько картинок.
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(len(images)))

	// Смещение первой картинки: после заголовка и всех описаний.
	offset := 6 + 16*len(images)
	for i, s := range sizes {
		// 256 в один байт не влезает, и формат договорился писать там ноль.
		b := byte(s)
		if s >= 256 {
			b = 0
		}
		buf.WriteByte(b)                                    // ширина
		buf.WriteByte(b)                                    // высота
		buf.WriteByte(0)                                    // цветов в палитре: у нас их нет
		buf.WriteByte(0)                                    // зарезервировано
		binary.Write(&buf, binary.LittleEndian, uint16(1))  // плоскостей
		binary.Write(&buf, binary.LittleEndian, uint16(32)) // бит на точку
		binary.Write(&buf, binary.LittleEndian, uint32(len(images[i])))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(images[i])
	}
	for _, img := range images {
		buf.Write(img)
	}

	path := filepath.Join("internal", "desktop", "icon.ico")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf("Готово: %s (%d байт, размеры %v)\n", path, buf.Len(), sizes)
	return nil
}

// draw возвращает точки значка построчно сверху вниз, по четыре байта
// (R, G, B, прозрачность) на точку.
//
// Каждая точка считается по шестнадцати подточкам: края ноты и скругления
// проходят не по границе точек, и без такого усреднения они выглядят
// лесенкой — особенно заметно на 16 точках в трее.
func draw(size int) []uint8 {
	pixels := make([]uint8, size*size*4)
	scale := float64(size) / 32 // рисунок задан в сетке 32×32, как SVG

	const sub = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var a, r, g, b float64
			for jy := 0; jy < sub; jy++ {
				for jx := 0; jx < sub; jx++ {
					sx := (float64(x) + (float64(jx)+0.5)/sub) / scale
					sy := (float64(y) + (float64(jy)+0.5)/sub) / scale
					c, ok := paint(sx, sy)
					if !ok {
						continue
					}
					a++
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
				}
			}
			i := (y*size + x) * 4
			if a > 0 {
				pixels[i+0] = uint8(r/a + 0.5)
				pixels[i+1] = uint8(g/a + 0.5)
				pixels[i+2] = uint8(b/a + 0.5)
			}
			pixels[i+3] = uint8(a/(sub*sub)*255 + 0.5)
		}
	}
	return pixels
}

// paint — цвет точки в сетке 32×32, или false, если точка прозрачная.
// Повторяет web/favicon.svg: жёлтый квадрат, на нём чёрная восьмая нота.
func paint(x, y float64) (rgb, bool) {
	if !inRoundedSquare(x, y, 32, 5) {
		return rgb{}, false
	}
	if inNote(x, y) {
		return ink, true
	}
	return yellow, true
}

// inNote — головка (наклонный овал), штиль и флажок.
func inNote(x, y float64) bool {
	// Головка: овал 6.5×4.8 с центром (15, 22.5), повёрнут на −22°.
	const angle = -22 * math.Pi / 180
	dx, dy := x-15, y-22.5
	u := dx*math.Cos(angle) + dy*math.Sin(angle)
	v := -dx*math.Sin(angle) + dy*math.Cos(angle)
	if (u*u)/(6.5*6.5)+(v*v)/(4.8*4.8) <= 0.25 {
		return true
	}
	// Штиль: от головки вверх.
	if x >= 16.6 && x <= 19.4 && y >= 6 && y <= 22 {
		return true
	}
	// Флажок: толстая полоса, спадающая вправо вниз.
	if x >= 19 && x <= 25.5 {
		top := 6 + (x-19)*0.75
		if y >= top && y <= top+4 {
			return true
		}
	}
	return false
}

// inRoundedSquare — попала ли точка внутрь квадрата со скруглёнными углами.
func inRoundedSquare(x, y, side, radius float64) bool {
	if x < 0 || y < 0 || x > side || y > side {
		return false
	}
	// Ближайший угол: если точка не в угловой четверти, она внутри заведомо.
	dx := math.Max(radius-x, x-(side-radius))
	dy := math.Max(radius-y, y-(side-radius))
	if dx <= 0 || dy <= 0 {
		return true
	}
	return math.Hypot(dx, dy) <= radius
}

// dib укладывает точки так, как их ждёт Windows внутри .ico: заголовок
// BITMAPINFOHEADER, потом цвета снизу вверх в порядке B, G, R, прозрачность,
// потом маска прозрачности из старых времён — она не нужна, но без неё
// некоторые части системы значок не читают.
func dib(pixels []uint8, size int) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(40))    // размер заголовка
	binary.Write(&buf, binary.LittleEndian, int32(size))   // ширина
	binary.Write(&buf, binary.LittleEndian, int32(size*2)) // высота: цвета + маска
	binary.Write(&buf, binary.LittleEndian, uint16(1))     // плоскостей
	binary.Write(&buf, binary.LittleEndian, uint16(32))    // бит на точку
	binary.Write(&buf, binary.LittleEndian, uint32(0))     // без сжатия
	binary.Write(&buf, binary.LittleEndian, uint32(size*size*4))
	for i := 0; i < 4; i++ {
		binary.Write(&buf, binary.LittleEndian, uint32(0)) // разрешение и палитра
	}

	for y := size - 1; y >= 0; y-- {
		for x := 0; x < size; x++ {
			i := (y*size + x) * 4
			buf.WriteByte(pixels[i+2]) // B
			buf.WriteByte(pixels[i+1]) // G
			buf.WriteByte(pixels[i+0]) // R
			buf.WriteByte(pixels[i+3]) // прозрачность
		}
	}

	// Маска: строка выравнивается по четыре байта. Нули означают «точка видна»,
	// настоящая прозрачность берётся из четвёртого байта цвета.
	rowBytes := ((size + 31) / 32) * 4
	buf.Write(make([]byte, rowBytes*size))
	return buf.Bytes()
}
