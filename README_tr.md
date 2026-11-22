[English](README.md) | **Türkçe**

# Pocketlab

*Önceki adı PhoneForge.*

[![CI](https://github.com/hakanngul/pocketlab/actions/workflows/ci.yml/badge.svg)](https://github.com/hakanngul/pocketlab/actions/workflows/ci.yml)
[![Release](https://github.com/hakanngul/pocketlab/actions/workflows/release.yml/badge.svg)](https://github.com/hakanngul/pocketlab/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/hakanngul/pocketlab)](https://github.com/hakanngul/pocketlab/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Yapay zekâ kodlama ajanlarının ve insanların gerçek iOS/Android cihazları, iOS simülatörlerini ve web sayfalarını görüp
yönetmesini sağlayan yerel bir araç. Web arayüzü, REST API, MCP sunucusu ve CLI içeren tek bir Go sunucusu olarak çalışır.

Pocketlab, Go ile sıfırdan yazılmış özgün bir yazılımdır (masaüstü uygulaması: Wails v3). Açık bileşenleri kullanır (adb, go-ios,
[appium/WebDriverAgent](https://github.com/appium/WebDriverAgent)) ve hiçbir ticari ürünle bağlantılı değildir.

## Özellikler

**Cihazlar ve ekran yansıtma**
- adb üzerinden Android (cihazda ek bir ajan gerekmez), USB üzerinden gerçek iOS cihazlar, iOS simülatörleri ve adresle eklenen uzak Pocketlab düğümleri.
- FPS ayarlı canlı yansıtma, çoklu cihaz ızgarası, tıklayarak dokunma ve UI ağacını inceleyen Element Picker.
- Hareketler: dokunma, çift dokunma, uzun basma, kaydırma, sürükleme, pinch, yazı girme, donanım tuşları, URL açma, pano, klavyeyi kapatma.
- Uygulama kurma, kaldırma, açma ve durdurma; çalışma zamanı izinleri; log akışı; ekran kaydı.
- Ekrandaki yazıyı OCR ile okuma (macOS'ta Apple Vision, yedek olarak tesseract); metne göre bulma ve dokunma.
- Android'de CDP üzerinden Chrome ve debug edilebilir WebView otomasyonu (gezinme, eval, tıklama, yazma).

**Otomasyon ve test**
- JSON DSL (sürüm 0.2) ve okunaklı `.mob` betik formatı; seçiciler, bekleme, doğrulama, parametre ve gizli değer desteği.
- Test projeleri, test çalıştırıcı ve çalıştırma geçmişi; arayüzden, REST API'den veya `pocketlab run` ile.

**AI ajanları**
- Cihazı Claude Code, Codex, Gemini, Cursor, Kiro veya kiro-cli üzerinden ya da yerleşik ajanla (Anthropic API anahtarı veya yerel Ollama) yöneten uygulama içi sohbet kutusu.
- `/mcp` adresinde 48 araçlık MCP sunucusu ve eklenti olarak paketlenmiş altı Claude Code skill'i.

**Hata ayıklama**
- Android ve iOS cihazlar ile simülatörler için çökme raporları.
- iOS için lldb oturumları (bağlanma veya başlatma, breakpoint, adım adım ilerleme, eval); yalnızca macOS.

**Arayüzler**
- Web arayüzü, REST API (`/api/v1`), MCP (`/mcp`), `pocketlab` CLI ve yerel masaüstü uygulaması.

## Platform desteği

| | macOS | Linux | Windows (yeni, henüz gerçek cihazda denenmedi) |
|---|---|---|---|
| Sunucu, CLI, web arayüzü, MCP | evet | evet | evet |
| Masaüstü uygulaması | evet (imzalı, notarize edilmiş) | hayır | deneysel, imzasız zip |
| Android gerçek cihazlar | evet | evet | evet (`adb.exe` `PATH` içinde olmalı) |
| iOS gerçek cihazlar | evet | evet (usbmuxd) | evet; USB için Apple Devices veya iTunes gerekir, WebDriverAgent bir kez bir Mac'ten kurulmalı |
| iOS simülatörleri | evet | hayır | hayır |
| lldb hata ayıklayıcı, yerel USB yakalama, Vision OCR | evet | hayır | hayır |

macOS dışındaki sistemlerde simülatör özellikleri "iOS simulators require macOS" hatası döndürür.

## Kurulum

**Masaüstü uygulaması (macOS).** [Releases](https://github.com/hakanngul/pocketlab/releases/latest) sayfasından
`Pocketlab-<sürüm>-macos-<mimari>.dmg` dosyasını (arm64 veya amd64) indirin ve `Pocketlab.app`'i Applications klasörüne sürükleyin.
Sürüm paketleri imzalı ve notarize edilmiştir. Uygulamanın `.zip` hali de yayınlanır.

**CLI ve sunucu.** macOS, Linux veya Windows için (amd64, arm64) `pocketlab_<sürüm>_<os>_<mimari>.tar.gz` dosyasını
(Windows'ta `.zip`) indirin, açın ve `./pocketlabd` ile `./pocketlab` komutlarını çalıştırın. Her sürümde `SHA256SUMS.txt` bulunur.
Deneysel Windows masaüstü uygulaması `Pocketlab-<sürüm>-windows-amd64.zip` dosyasıdır ve WebView2 çalışma zamanını gerektirir.

**CLI kurulum betiği (depo klonundan).**

```sh
scripts/install-cli.sh          # bin/pocketlab'u derler ve ~/.local/bin veya /usr/local/bin içine bağlar
scripts/install-cli.sh --copy   # sembolik bağ yerine kopyalar
```

**Kaynaktan derleme.** Go 1.27 veya üstü gerekir (`go.mod` dosyasına bakın).

```sh
go build -o bin/pocketlabd ./cmd/pocketlabd
go build -o bin/pocketlab ./cmd/pocketlab
scripts/build-desktop.sh --dmg   # macOS masaüstü uygulaması, Xcode komut satırı araçları gerekir
```

## Hızlı başlangıç

```sh
./pocketlabd                     # web arayüzünü, REST API'yi ve MCP'yi 127.0.0.1:8700 üzerinde sunar
open http://127.0.0.1:8700        # veya aynı sunucuyu çalıştıran masaüstü uygulamasını kullanın
pocketlab devices
```

Android için `PATH` içinde `adb` olmalı ve telefonda USB hata ayıklama açık olmalı. Cihazı bağlayın, arayüzde görünür.

**Gerçek iOS cihazlar** Pocketlab'un kendi imzalı WebDriverAgent'ı (WDA) ile yönetilir. Her şey uygulamanın içinde olur (Xcode ve bir Apple ID gerekir, ücretsiz olabilir): iPhone'u seçip bridge anahtarına tıklayın.
Yardımcı hiç derlenmediyse anahtar **Build WDA** yazar; Pocketlab onu Mac'inizde kendi Apple takımınızla derler (sabitlenmiş kaynak uygulamanın içinde gelir, repo
veya terminal gerekmez), sonra kurar ve başlatır. İstendiğinde telefonda geliştirici profiline güvenin. Arayüzsüz: `pocketlab ios build-wda [--sim] [--install <cihaz>] [--team <id>]`.
Gereksinimler ve sorun giderme: [docs/ios-setup.md](docs/ios-setup.md). macOS'ta açılmış iOS simülatörleri otomatik listelenir.

## AI ajanlarıyla kullanım

MCP sunucusunu kaydedin (önce `pocketlabd`'u başlatın):

```sh
claude mcp add --transport http pocketlab http://127.0.0.1:8700/mcp
```

Ya da hem MCP sunucusunu kaydeden hem de skill'leri yükleyen Claude Code eklentisini kurun:

```
/plugin marketplace add hakanngul/pocketlab
/plugin install pocketlab
```

Yalnızca skill'ler için: `scripts/install-skills.sh` (`skills/pocketlab-*` klasörlerini `~/.claude/skills` içine bağlar; `--copy`,
`--uninstall` ve `--dry-run` desteklenir). Skill'ler cihaz kontrolü, duman testi, erişilebilirlik denetimi, mağaza ekran görüntüleri,
test yazımı ve CLI kullanımını kapsar. Ayrıntılar: [docs/skills.md](docs/skills.md).

**Uygulama içi sohbet kutusu.** AI Configuration panelinden bir ajan seçin: Claude Code, OpenAI Codex CLI, Gemini CLI, Cursor Agent, Kiro
(IDE, yalnızca bağlanır), Kiro CLI veya yerleşik ajan. **Connect**, Pocketlab'un MCP sunucusunu seçilen araca kaydeder. Yerleşik ajan bir
Anthropic API anahtarı ya da yerel bir Ollama modeli kullanır (`OLLAMA_HOST`, `OLLAMA_MODEL`). Bazı ajanlar uygulanmış olsa da uçtan uca
denenmemiştir; nelerin doğrulandığı için [BACKLOG.md](BACKLOG.md) dosyasına bakın.

## CLI örnekleri

```sh
pocketlab devices
pocketlab -d iphone screenshot -o home.png
pocketlab -d sm-g tap --text "Settings"
pocketlab tree --filter "Wi-Fi"
pocketlab swipe up
pocketlab type "hello"
pocketlab launch com.example.app
pocketlab install app.apk
pocketlab ocr --find "Sign in"
pocketlab run login.mob
pocketlab crashes --app com.example.app
pocketlab agent "open Settings and turn on Dark Mode"
```

`-d`, cihaz kimliği, seri numarası, ad veya benzersiz bir önekle eşleşir. `--json` makine tarafından okunabilir çıktı verir. Çıkış kodları:
0 başarılı, 1 hata, 2 kullanım hatası, 3 cihaz bulunamadı veya belirsiz, 4 sunucuya ulaşılamadı. Tüm komutlar: [docs/cli.md](docs/cli.md).

**Sunucu yaşam döngüsü.** Her başlatma ve durdurma, nedeniyle birlikte `~/.pocketlab/server-events.jsonl` dosyasına yazılır (kapatma API'si ve
isteyen taraf, sinyal, çökme, port kullanımda ya da `kill -9` gibi beklenmeyen çıkış). `pocketlab server history` ile, web arayüzündeki
**Server** panelinde veya masaüstü uygulamasında (Server menüsü: başlat, durdur, yeniden başlat) görebilirsiniz. `pocketlab server start`,
`pocketlabd`'i arka planda başlatır.

## Test yazma

Bir `.mob` betiğinde her satır bir ifadedir:

```
# Param: email = qa@example.com
# Alerts: accept

app "com.example.app" fresh
wait_for ~"Sign in" timeout:10000
type ~"Email" -> "${email}" clear
type_secret ~"Password" -> "login-password"
tap "Sign in" type:button
assert_exists ~"Welcome" timeout:5000
scroll down to "Log out"
tap "Log out"
assert_not_exists ~"Welcome" timeout:3000
```

Betiği `pocketlab run login.mob` ile, arayüzdeki bir test projesinden veya MCP'nin `test_run` / `execute_dsl` araçlarıyla çalıştırabilirsiniz.
`type_secret`, parolanın kendisini değil, kimlik bilgisi deposundaki gizli değer kimliğini alır. Tüm söz dizimi:
[skills/pocketlab-test-authoring/references/mob-syntax.md](skills/pocketlab-test-authoring/references/mob-syntax.md).

## Yapılandırma

| Değişken | Anlamı |
|---|---|
| `POCKETLAB_ADDR` | masaüstü uygulamasının dinleme adresi (varsayılan `127.0.0.1:8700`); `pocketlabd` için `-addr` kullanılır |
| `POCKETLAB_HOME` | durum dizini (varsayılan `~/.pocketlab`) |
| `POCKETLAB_URL`, `POCKETLAB_DEVICE`, `POCKETLAB_TOKEN` | CLI varsayılanları: sunucu URL'si, hedef cihaz, API belirteci (sunucu tarafı için `pocketlabd -token`) |
| `PL_MJPEG_FPS`, `PL_MJPEG_SCALE`, `PL_MJPEG_QUALITY` | iOS WDA MJPEG akışı (varsayılanlar 60 / 25 / 50; aralıklar 1-60 / 1-100 / 1-100) |
| `PL_ANDROID_AUTOBRIDGE=0` | ilk UI ağacı okumasında Android UiAutomator2 köprüsünü başlatma |
| `PL_SIM_AUTOWDA=0` | simülatörlerde WDA'yı ihtiyaç anında başlatma |
| `PL_TEAM_ID`, `PL_BUNDLE_PREFIX` | uygulama içi WDA derlemesi için imzalama ayarları (arayüzde girilen Team ID `~/.pocketlab/settings.json` içine kaydedilir; `PL_TEAM_ID` önceliklidir); `PL_WDA_HOME`, `PL_WDA_TARBALL` WDA çalışma klasörünü / kaynak arşivini değiştirir |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL`, `OLLAMA_HOST`, `OLLAMA_MODEL` | yerleşik ajanın sağlayıcıları |

`pocketlabd` bayrakları: `-addr`, `-adb`, `-token`, `-webdir`.

PhoneForge'dan yükseltme: sunucu, CLI ve masaüstü uygulaması ilk açılışta (`~/.pocketlab` yoksa) `~/.phoneforge`
dizinini `~/.pocketlab` olarak taşır. Eski `PHONEFORGE_*` / `PF_*` ortam değişkenleri ve `X-PhoneForge-*` HTTP başlıkları
yedek olarak hâlâ geçerlidir; ikisi birden tanımlıysa yeni `POCKETLAB_*` / `PL_*` adları önceliklidir.
Yapay zekâ ajanını AI Yapılandırması sayfasından yeniden bağladığınızda eski `phoneforge` MCP girdisi `pocketlab` ile değiştirilir.

## Dokümantasyon

- [docs/cli.md](docs/cli.md): CLI komutları, cihaz çözümleme, çıkış kodları
- [docs/ios-setup.md](docs/ios-setup.md): iPhone'lar için WebDriverAgent kurulumu
- [docs/desktop.md](docs/desktop.md): Wails masaüstü uygulaması
- [docs/debugging.md](docs/debugging.md): çökme raporları ve lldb oturumları
- [docs/skills.md](docs/skills.md): Claude Code skill'leri ve eklentisi
- [docs/release.md](docs/release.md): sürüm paketleri, imzalama ve notarization
- [FEATURES.md](FEATURES.md): ayrıntılı özellik listesi
- [RUN.md](RUN.md): çalıştırma notları

## Yol haritası

Açık işler, doğrulama durumu ve bilinen eksikler [BACKLOG.md](BACKLOG.md) dosyasında tutulur.

## Katkı ve lisans

Issue ve pull request'ler memnuniyetle karşılanır. Değişiklik göndermeden önce `go test ./...` çalıştırın. Pocketlab [MIT Lisansı](LICENSE) ile yayınlanmaktadır.
