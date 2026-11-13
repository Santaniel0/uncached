# uncached

[![CI](https://github.com/Santaniel0/uncached/actions/workflows/ci.yml/badge.svg)](https://github.com/Santaniel0/uncached/actions/workflows/ci.yml)

`uncached` — обёртка над `go test` для CI. Она запускает тесты как обычно, а
потом для каждого пакета, который не взялся из тестового кэша (нет `(cached)`),
пишет, почему: какой исходный файл или версия модуля поменялись, какая
переменная окружения, какой файл из `testdata` и как именно (содержимое или
только mtime), какой флаг отключил кэш. Для самой частой причины — mtime после
свежего `git clone` — есть команда `restore-mtime`.

## Проблема

Типичная ситуация: в CI кэшируется `GOCACHE` (actions/setup-go делает это по
умолчанию), коммит меняет только README, а `go test ./...` всё равно гоняет
половину пакетов заново. Ожидание было, что неизменённые пакеты выдадут
`(cached)`.

Причина в том, как устроен тестовый кэш. Ключ результата — это не только
бинарник теста, но и всё, что тест прочитал во время прогона: каждая
переменная окружения через `os.Getenv` и каждый файл через `os.Open`/`os.Stat`
внутри модуля. Для файлов `go` хэширует не содержимое, а размер, права и
**mtime** (`hashWriteStat` в `src/cmd/go/internal/test/test.go`). `git` mtime не
хранит, и после `git clone` у всех файлов mtime равен времени клона. Значит,
любой тест, читающий `testdata/`, в каждом новом CI-джобе промахивается мимо
кэша, хотя ничего не изменилось.

Причины бывают и другие: тест читает `GITHUB_RUN_ID`, кто-то добавил
`-count=1` в Makefile, восстановился старый `GOCACHE`, тест сам пишет файл
перед чтением. Снаружи всё это выглядит одинаково — `(cached)` просто нет.
Встроенный способ разбираться — `GODEBUG=gocachetest=1`. Но при изменившихся
входах он печатает только `test output not found: cache entry not found` и не
говорит, какой именно вход изменился. Дальше приходится сравнивать вывод
`GODEBUG=gocachehash=1` двух прогонов вручную. Обсуждения в трекере Go:
[golang/go#35301](https://github.com/golang/go/issues/35301) (открыт с 2019 года,
там прямо просят инструмент для этого), [#58571](https://github.com/golang/go/issues/58571)
(mtime и CI), [#52573](https://github.com/golang/go/issues/52573) (mtime каталога
`.git`, который трогал `git status` в промпте шелла).

## Установка

Готовые сборки для Linux, Windows и macOS лежат на странице [Releases](https://github.com/Santaniel0/uncached/releases/latest), Go для них не нужен. Собрать самому через Go:

```
go install github.com/Santaniel0/uncached/cmd/uncached@latest
```

Для сборки нужен Go 1.27.

## Использование

```
uncached run [флаги] [--] [флаги go test] [пакеты]
uncached explain [-state FILE] [-json] [-q]
uncached diff [-json] OLD_STATE NEW_STATE
uncached restore-mtime [-state FILE] [-all] [-n]
```

- `run` запускает `go test` с переданными аргументами. Вывод `go test`
  проходит без изменений (в том числе `-json` на stdout), отчёт печатается в
  stderr. Код возврата — код `go test`. Ошибки самого анализа выводятся
  предупреждениями и код возврата не меняют. Флаги: `-state` (где хранить
  состояние), `-report-json FILE` (отчёт ещё и в JSON), `-content-limit`
  (до какого размера хэшировать содержимое файлов, по умолчанию 4MiB), `-q`
  (только итоговая строка и заметки).
- `explain` печатает отчёт последнего `run` ещё раз, текстом или в JSON.
- `diff` сравнивает два файла состояния, например артефакты двух CI-прогонов.
- `restore-mtime` ставит файлам, которые тесты читали в прошлых прогонах,
  mtime последнего коммита, где они менялись; каталогам — самый поздний mtime
  внутри них. С `-all` обрабатываются все файлы в репозитории, `-n` только
  показывает, что было бы сделано.

Флаги `go test` идут после `--`: `uncached run -- -race -count=1 ./...`.

Состояние по умолчанию лежит внутри `GOCACHE`, в подкаталоге `uncached/`. CI,
который уже сохраняет `GOCACHE` между прогонами, сохранит и его. `go clean
-cache` и автоматическая чистка кэша трогают только подкаталоги `00`–`ff`
(`src/cmd/go/internal/clean/clean.go`, `trim` в
`src/cmd/go/internal/cache/cache.go`), так что этот каталог переживает обе.

Пример для GitHub Actions:

```yaml
- uses: actions/checkout@v5
  with:
    fetch-depth: 0        # restore-mtime нужна история коммитов
- uses: actions/setup-go@v6
  with:
    go-version: "1.27.x"
    cache: false
# Ключ встроенного кэша setup-go строится из хэша go.sum, и при попадании
# кэш не пересохраняется. GOCACHE лучше сохранять после каждого прогона.
- uses: actions/cache@v4
  with:
    path: ~/.cache/go-build
    key: gocache-${{ runner.os }}-${{ github.sha }}
    restore-keys: gocache-${{ runner.os }}-
- run: go install github.com/Santaniel0/uncached/cmd/uncached@latest
- run: uncached restore-mtime
- run: uncached run -- -race ./...
```

## Пример

`scripts/demo.sh` воспроизводит CI на копии `examples/demo`. В демо-модуле
четыре пакета: `invoice` сверяется с golden-файлом, `catalog` читает каталог
`testdata`, `config` читает переменную `DEMO_REGION`, `money` ничего не читает.
Каждый «джоб» заново клонирует репозиторий в один и тот же каталог, а
`GOCACHE` между джобами общий. Ниже — вывод реального запуска (go1.27.3,
Windows, Git Bash), начиная со второго джоба. В первом все четыре пакета
помечены как новые.

```
### CI job 2: fresh clone of the same commit, GOCACHE restored from job 1

$ uncached run ./...
ok  	example.com/demo/catalog	1.034s
ok  	example.com/demo/config	(cached)
ok  	example.com/demo/invoice	0.869s
ok  	example.com/demo/money	(cached)

uncached: 2 of 4 packages cached; compared with the run of 2026-11-09 21:34:48 UTC (commit 826198720ce8)

example.com/demo/catalog
  test inputs changed, test binary unchanged:
    catalog/testdata/: listing changed: new mtime, same content: apples.json, pears.json
    catalog/testdata/apples.json: mtime changed, content identical (2026-11-09 21:34:41 UTC -> 2026-11-09 21:34:49 UTC)
    catalog/testdata/pears.json: mtime changed, content identical (2026-11-09 21:34:41 UTC -> 2026-11-09 21:34:49 UTC)

example.com/demo/invoice
  test inputs changed, test binary unchanged:
    invoice/testdata/invoice.golden: mtime changed, content identical (2026-11-09 21:34:41 UTC -> 2026-11-09 21:34:49 UTC)

note: 2 packages missed the cache only because file mtimes changed while the content stayed
      the same. A fresh git checkout does exactly this. Run `uncached restore-mtime` before
      go test so that mtimes match between checkouts.
```

В третьем джобе `restore-mtime` переводит mtime на время коммита. Это ещё
один промах: результаты в кэше записаны с mtime второго клона. Начиная с
четвёртого джоба mtime после клона и восстановления каждый раз одинаковые:

```
### CI job 4: fresh clone, restore-mtime, tests

$ uncached restore-mtime
uncached: restored mtimes of 3 files and 1 directory from 1 commit in 90ms
$ uncached run ./...
ok  	example.com/demo/catalog	(cached)
ok  	example.com/demo/config	(cached)
ok  	example.com/demo/invoice	(cached)
ok  	example.com/demo/money	(cached)

uncached: 4 of 4 packages cached; compared with the run of 2026-11-09 21:35:05 UTC (commit 826198720ce8)
```

Коммит, который меняет код, и джоб с новой переменной окружения:

```
### CI job 5: a commit refactors money.go

$ uncached restore-mtime
uncached: restored mtimes of 3 files and 1 directory from 2 commits in 91ms
$ uncached run ./...
ok  	example.com/demo/catalog	(cached)
ok  	example.com/demo/config	(cached)
ok  	example.com/demo/invoice	0.378s
ok  	example.com/demo/money	0.377s

uncached: 2 of 4 packages cached; compared with the run of 2026-11-09 21:35:10 UTC (commit 826198720ce8)

example.com/demo/invoice
  test binary changed:
    example.com/demo/money: money.go modified

example.com/demo/money
  test binary changed:
    example.com/demo/money: money.go modified

### CI job 6: the job sets DEMO_REGION

$ uncached restore-mtime
uncached: restored mtimes of 3 files and 1 directory from 2 commits in 91ms
$ export DEMO_REGION=us-east
$ uncached run ./...
ok  	example.com/demo/catalog	(cached)
ok  	example.com/demo/config	0.372s
ok  	example.com/demo/invoice	(cached)
ok  	example.com/demo/money	(cached)

uncached: 3 of 4 packages cached; compared with the run of 2026-11-09 21:35:15 UTC (commit dd46039272d2)

example.com/demo/config
  test inputs changed, test binary unchanged:
    env DEMO_REGION: now set
```

Последний джоб запускает тесты с `-count=1`, и для каждого пакета там написано
`caching disabled by the test flag -count=1`.

## Как это работает

Поиск результата в тестовом кэше у `go` двухуровневый (`tryCacheWithID` в
`src/cmd/go/internal/test/test.go`). Сначала из бинарника теста и
«кэшируемых» флагов (`-run`, `-short`, `-v` и ещё несколько) считается test ID.
По нему в кэше лежит список входов последнего прогона: какие переменные тест
читал, какие файлы открывал. Этот список пишет сам тестовый бинарник, формат
описан в `src/testing/internal/testdeps/deps.go`. Затем `go` хэширует текущие
значения этих входов (`computeTestInputsID`) и ищет результат по паре (test ID,
хэш входов). Если какой-то вход изменился, второй поиск промахивается.

`uncached run` запускает `go test` с `GODEBUG=gocachetest=1` и разбирает строки
`testcache:` из stderr. Остальной stderr проходит к пользователю. Из этих строк
видно, на каком шаге был промах. После прогона `uncached` берёт test ID
сохранённого результата и читает список входов прямо из `GOCACHE`. Формат
записей `<id>-a` и `<id>-d` взят из `src/cmd/go/internal/cache/cache.go`. Пути
разрешаются так же, как это делает `go`: относительно каталога пакета, с учётом
`chdir`, а файлы вне корня модуля отбрасываются. Для каждого входа сохраняется
то, что хэширует `go` (размер, права, mtime, для открытых каталогов — список
записей), и в дополнение SHA-256 содержимого файлов до 4 MiB и имён в
каталогах. По этому хэшу отличается «изменился только mtime» от «изменилось
содержимое». Значения переменных окружения сохраняются только как SHA-256.

В следующем `run` до запуска `go test` записанные входы снимаются ещё раз —
именно с этими значениями `go` будет сравнивать. Дальше всё зависит от того,
что напечатал `go`:

- «input list not found» — для такого test ID в кэше ничего нет, то есть
  поменялся бинарник или флаги. `uncached` сравнивает отпечаток из `go list
  -deps -test -json`: хэши файлов всех локальных пакетов в графе
  зависимостей теста, версии модулей, переменные `go env`, влияющие на сборку,
  и флаги. Если test ID совпадает с записанным, значит, изменился не
  бинарник, а пропала запись в `GOCACHE`.
- «test output not found» — бинарник тот же, изменились входы. Выводится
  список изменённых входов.
- строки про `-count=1`, про `go test` без аргументов-пакетов, про файл моложе
  двух секунд и про `go clean -testcache` переводятся в отдельные причины.

`restore-mtime` один раз проходит `git log --name-only` от новых коммитов к
старым и останавливается, как только для всех нужных файлов найдено время
последнего изменения. Каталогам ставится самое позднее время среди файлов
внутри. В результате mtime зависят только от коммита, и следующий свежий клон
того же коммита даёт те же значения.

## Ограничения

- Формат отладочного вывода, формат записей `GOCACHE` и формат списка входов —
  внутренние детали `cmd/go`. Всё проверено только на go1.27.3 (Windows).
  CI настроен на ubuntu с последним 1.27.x. Строки `testcache:`, которые
  `uncached` не узнаёт, считаются и выводятся заметкой, чтобы смена формата
  была видна.
- Значение `GODEBUG` входит в ключ тестового кэша, а `uncached` добавляет туда
  `gocachetest=1`. Поэтому результаты прогонов через `uncached` и через
  обычный `go test` друг другу не подходят. Они хранятся рядом и друг друга не
  вытесняют, но в CI тесты стоит всегда запускать одинаково.
- С `GOCACHEPROG` список входов прочитать нельзя: причины на уровне файлов и
  переменных пропадают, остаются только причины на уровне бинарника.
- Отпечаток бинарника приблизительный. Если `go` считает бинарник новым, а в
  исходниках, версиях модулей, `go env` и флагах разницы нет, `uncached` так и
  пишет и причину не угадывает. Обычно это C-компилятор для cgo или что-то,
  что читает компилятор, но не видит `go list`.
- Файлы больше `-content-limit` не хэшируются. Для них пишется «mtime changed,
  content not compared».
- Для путей сохраняется результат `Stat`, без `Lstat`. Смена цели симлинка с
  теми же размером и mtime не будет классифицирована.
- `uncached` объясняет только промахи. Если файл поменялся, а размер и mtime
  остались прежними, `go` вернёт устаревший `(cached)`, и `uncached` этого не
  заметит.
- `restore-mtime` в shallow clone (по умолчанию в actions/checkout) ставит
  файлам, изменённым до границы клона, время граничного коммита. Оно меняется
  с каждым новым коммитом. Нужен `fetch-depth: 0`, иначе команда предупредит.
- SHA-256 значений переменных окружения без соли. Короткое секретное значение
  можно подобрать по файлу состояния. Файл лежит в `GOCACHE` рядом со
  скомпилированными пакетами, и доступ к нему стоит ограничивать так же.

## Лицензия

MIT, см. [LICENSE](LICENSE).
