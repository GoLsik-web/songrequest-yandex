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
// Рисунок повторяет web/favicon.svg: тёмный квадрат со скруглением и лаймовая
// точка «в эфире» посередине — те же краски, что в панели «Эфир».
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Краски из дизайна «Эфир». Меняются вместе с web/favicon.svg, не по отдельности.
var (
	dark = rgb{0x08, 0x09, 0x0a}
	lime = rgb{0xc8, 0xf7, 0x51}
)

type rgb struct{ R, G, B uint8 }

// sizes — все размеры значка в одном файле. Windows сам берёт подходящий:
// 16 для трея, 32 для окна, 128 для Alt+Tab на крупном экране. Если нужного
// размера в файле нет, система растянет ближайший, и края расползутся.
var sizes = []int{16, 20, 24, 32, 48, 64, 128}

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
// Каждая точка считается по шестнадцати подточкам: край скругления и край
// круга проходят не по границе точек, и без такого усреднения они выглядят
// лесенкой — особенно заметно на 16 точках в трее.
func draw(size int) []uint8 {
	pixels := make([]uint8, size*size*4)
	scale := float64(size) / 32 // масштаб от исходных 32 точек в SVG
	corner := 6 * scale
	dot := 6 * scale
	center := float64(size) / 2

	const sub = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var cover, inDot float64
			for jy := 0; jy < sub; jy++ {
				for jx := 0; jx < sub; jx++ {
					sx := float64(x) + (float64(jx)+0.5)/sub
					sy := float64(y) + (float64(jy)+0.5)/sub
					if inRoundedSquare(sx, sy, float64(size), corner) {
						cover++
					}
					if math.Hypot(sx-center, sy-center) <= dot {
						inDot++
					}
				}
			}
			total := float64(sub * sub)
			cover /= total
			inDot /= total

			// Круг лежит на тёмном квадрате, поэтому сначала смешиваем краски,
			// а потом умножаем на прозрачность самого квадрата.
			limeShare := inDot
			if limeShare > cover {
				limeShare = cover
			}
			alpha := cover
			var r, g, b float64
			if alpha > 0 {
				darkPart := (cover - limeShare) / alpha
				limePart := limeShare / alpha
				r = float64(dark.R)*darkPart + float64(lime.R)*limePart
				g = float64(dark.G)*darkPart + float64(lime.G)*limePart
				b = float64(dark.B)*darkPart + float64(lime.B)*limePart
			}
			i := (y*size + x) * 4
			pixels[i+0] = uint8(r + 0.5)
			pixels[i+1] = uint8(g + 0.5)
			pixels[i+2] = uint8(b + 0.5)
			pixels[i+3] = uint8(alpha*255 + 0.5)
		}
	}
	return pixels
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
