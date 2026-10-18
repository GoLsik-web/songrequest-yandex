# Сборка релизного .exe одной командой:
#
#   .\build.ps1
#
# Кладёт готовый файл в dist\songrequest.exe и собирает архив с инструкцией,
# который можно сразу отправить стримеру.
#
# Параметры:
#   -Version 0.2.0   вписать конкретную версию вместо автоматической
#   -NoZip           не собирать архив, нужен только .exe
#   -SkipTests       пропустить тесты (по умолчанию они обязательны)
#   -Flavor панель    отдельная сборка рядом с рабочей: своя папка настроек,
#                     свой порт, своё имя файла. Нужна, чтобы пробовать новое,
#                     не трогая то, что уже работает у людей.

param(
    [string]$Version,
    [string]$Flavor,
    [switch]$NoZip,
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

# Go мог быть установлен уже после открытия этого окна — подхватываем PATH.
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    $env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" +
                [Environment]::GetEnvironmentVariable("Path", "User")
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go не найден. Установи его: winget install GoLang.Go"
}

# Версия: короткий хеш коммита, чтобы по логу от стримера было понятно, какая
# именно сборка у него стоит. Без git — просто дата.
if (-not $Version) {
    $commit = ""
    try { $commit = (git rev-parse --short HEAD 2>$null) } catch {}
    if ($commit) {
        $dirty = ""
        if ((git status --porcelain 2>$null)) { $dirty = "-грязная" }
        $Version = "$(Get-Date -Format 'yyyy.MM.dd')-$commit$dirty"
    } else {
        $Version = Get-Date -Format 'yyyy.MM.dd-HHmm'
    }
}

if (-not $SkipTests) {
    Write-Host "Тесты…" -ForegroundColor Cyan
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw "Тесты не прошли — сборка остановлена." }
}

Write-Host "Сборка $Version…" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path dist | Out-Null

# Пробная сборка получает своё имя файла: иначе, скачанная в ту же папку, она
# затрёт рабочую, и человек об этом узнает не сразу.
$name = if ($Flavor) { "songrequest-$Flavor" } else { "songrequest" }
$exe = "dist\$name.exe"
# -s -w выкидывают отладочные таблицы: файл меньше почти на треть.
# -H=windowsgui убирает чёрное окно консоли: приложение теперь обычная
# программа со своим окном, и консоль ей не нужна. Печатать в неё поэтому
# некуда — про поломки при запуске приложение говорит окном с сообщением,
# всё остальное пишется в лог.
$env:CGO_ENABLED = "0"
$ldflags = "-s -w -H=windowsgui -X main.version=$Version"
if ($Flavor) { $ldflags += " -X main.flavor=$Flavor" }
go build -trimpath -ldflags $ldflags -o $exe .
if ($LASTEXITCODE -ne 0) { throw "Сборка не удалась." }

$size = [math]::Round((Get-Item $exe).Length / 1MB, 1)
Write-Host "Готово: $exe ($size МБ, версия $Version)" -ForegroundColor Green

if (-not $NoZip) {
    $zip = "dist\$name-$Version.zip"
    $staging = "dist\_pack"
    Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $staging | Out-Null

    Copy-Item $exe $staging
    # Инструкций в архиве больше нет.
    #
    # Раньше рядом с программой лежало семь файлов: пять инструкций в HTML и
    # два чек-листа. Все они писались до того, как в приложении появился мастер
    # первой настройки, и с тех пор рассказывают ровно то же самое, только
    # хуже: мастер ведёт по шагам, сам проверяет, что получилось, и знает, что
    # уже сделано, а файл этого не умеет. Человек, распаковавший архив с
    # восемью файлами, первым делом спрашивает «а что из этого открывать».
    #
    # Поэтому в архиве только программа и короткая записка. Сами файлы никуда
    # не делись — они лежат в docs/ и остаются источником текстов для мастера,
    # который сверяет с ними надписи (см. TestInstructionsQuoteRealLabels).
    @"
Привет!

Запусти songrequest.exe двойным щелчком — больше ничего распаковывать и
открывать не надо.

Приложение само проведёт по настройке: обход блокировок, Spotify, Twitch,
награда за баллы, виджет для OBS. По шагам, с картинками, по одному за раз.
Пройти её заново можно в любой момент: настройки -> Прочее -> «Пройти
настройку заново».

Что полезно знать сразу:

Крестик окно не закрывает, а прячет к часам, справа внизу: заказы продолжают
работать. Вернуть окно - щелчок по значку возле часов. Закрыть совсем -
правой кнопкой по значку и «Выход».

Модераторам канала отправь справочник команд: настройки -> Заказы ->
«Сохранить файл для модераторов».

Windows может сказать, что издатель неизвестен: «Подробнее» -> «Выполнить
в любом случае». Приложение просто без платной подписи.

Если что-то сломалось: настройки -> Прочее -> «Сохранить лог и историю».
В архиве будет всё, что нужно для разбора, а ключи доступа из него вырезаны.
"@ | Out-File -FilePath "$staging\Начни отсюда.txt" -Encoding utf8

    Remove-Item $zip -ErrorAction SilentlyContinue
    Compress-Archive -Path "$staging\*" -DestinationPath $zip
    Remove-Item -Recurse -Force $staging

    Write-Host "Архив для отправки: $zip" -ForegroundColor Green
}
