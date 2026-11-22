# iOS setup / iOS kurulumu

Pocketlab controls real iPhones through its own signed build of
[appium/WebDriverAgent](https://github.com/appium/WebDriverAgent) (BSD licence).

## English

Everything happens inside Pocketlab; you never need a terminal or the repo. The WebDriverAgent source
(pinned v16.15.3, BSD licence, licence file included) ships inside the app and is built on your own Mac,
because the runner must be signed with your own Apple team.

1. Install **Xcode** from the App Store and open it once. If Pocketlab says Xcode is not selected, run in Terminal
   (needs your password): `sudo xcode-select -s /Applications/Xcode.app/Contents/Developer`. If it reports the license,
   run `sudo xcodebuild -license accept`.
2. In **Xcode > Settings > Accounts** sign in with your Apple ID (free or paid). Pocketlab finds your team from the
   "Apple Development" certificate, provisioning profiles or Xcode preferences; if it cannot, it asks for your 10-character
   Team ID once and saves it (`~/.pocketlab/settings.json`; `PL_TEAM_ID` overrides).
3. On the iPhone: **Settings > Privacy & Security > Developer Mode > On** (the phone reboots), then confirm.
4. Connect the phone by cable, unlock it, tap **Trust**. Open Xcode once with the phone connected so it registers the device with your team.
5. In Pocketlab select the iPhone and click the bridge toggle (it reads **Build WDA** the first time). Pocketlab builds
   the helper (live progress and log), installs it on the phone and starts it.
6. If starting fails with an untrusted developer: **Settings > General > VPN & Device Management > your Apple Development profile > Trust**, then click Start again.

Headless / scripts: `pocketlab ios build-wda [--team ABCDE12345] [--install <device>]` (a running server is required).
Developers can still use `scripts/build-wda.sh`. Environment: `PL_TEAM_ID`, `PL_BUNDLE_PREFIX` (default `com.pocketlab`),
`PL_WDA_HOME` (default `~/.pocketlab/wda`), `PL_WDA_TARBALL` (path of the source tarball).
The tarball is looked up in `PL_WDA_TARBALL`, `<app>/Contents/Resources/`, next to the executable and `~/.pocketlab/wda/`;
if none exists Pocketlab downloads it from GitHub. Logs: `~/.pocketlab/wda/build.log`.

Pocketlab explains common failures in the UI: Xcode missing or not selected, no development team, phone not trusted or
Developer Mode off, device not registered with your team, and the free-account App ID limit.
Free accounts: profiles expire after 7 days (build and install again) and the number of app ids per week is limited.

## Turkce

Her sey Pocketlab icinde olur; terminal veya repo gerekmez. WebDriverAgent kaynagi (v16.15.3, BSD lisansi, lisans dosyasi dahil)
uygulamanin icinde gelir ve kendi Mac'inizde derlenir, cunku runner kendi Apple takiminizla imzalanmak zorundadir.

1. App Store'dan **Xcode**'u kurun ve bir kez acin. Pocketlab "Xcode secili degil" derse Terminal'de (sifre ister) calistirin:
   `sudo xcode-select -s /Applications/Xcode.app/Contents/Developer`. Lisans uyarisi gelirse `sudo xcodebuild -license accept`.
2. **Xcode > Settings > Accounts** bolumunden Apple ID ile giris yapin (ucretsiz veya ucretli). Pocketlab takiminizi "Apple Development"
   sertifikasindan, provisioning profillerinden veya Xcode tercihlerinden bulur; bulamazsa 10 karakterlik Team ID'nizi bir kez sorar ve kaydeder
   (`~/.pocketlab/settings.json`; `PL_TEAM_ID` onceliklidir).
3. iPhone'da: **Ayarlar > Gizlilik ve Guvenlik > Gelistirici Modu > Acik** (telefon yeniden baslar), onaylayin.
4. Telefonu kabloyla baglayin, kilidini acin, **Guven** deyin. Telefon bagliyken Xcode'u bir kez acin ki cihaz takiminiza kaydedilsin.
5. Pocketlab'da iPhone'u secip bridge anahtarina tiklayin (ilk seferde **Build WDA** yazar). Pocketlab yardimciyi derler (canli ilerleme ve log),
   telefona kurar ve baslatir.
6. Baslatma "guvenilmeyen gelistirici" ile basarisiz olursa: **Ayarlar > Genel > VPN ve Aygit Yonetimi > Apple Development profiliniz > Guven**, sonra tekrar Start.

Arayuzsuz: `pocketlab ios build-wda [--team ABCDE12345] [--install <cihaz>]` (calisan bir sunucu gerekir). Gelistiriciler `scripts/build-wda.sh` kullanmaya devam edebilir.
Degiskenler: `PL_TEAM_ID`, `PL_BUNDLE_PREFIX`, `PL_WDA_HOME`, `PL_WDA_TARBALL`. Log: `~/.pocketlab/wda/build.log`.

Ucretsiz hesaplarda profil 7 gun sonra dolar (yeniden derleyip kurun); haftalik app id sayisi sinirlidir.

## iOS Simulator

Simulators need no signing or phone. Build the simulator WDA once from the app
(**Integrations > iOS Simulators > Build WDA**, or `pocketlab ios build-wda --sim`; output: `~/.pocketlab/wda/build-sim`).

1. UI: **Integrations > iOS Simulators > Boot**, then **Start WDA**. Or REST:
   `POST /api/v1/simulators/{udid}/boot`, then `POST /api/v1/simulators/{udid}/wda/start`.
2. The booted simulator appears in the device list as `sim-<udid>` and works like an iPhone
   (screenshot, UI tree, tap, swipe, type, Home, app launch, install, open URL, MJPEG live view).
   Without WDA only screenshots, app list/launch/stop/install, open URL, clipboard and logs work (via `simctl`).
3. WDA ports are allocated per simulator from 8200 (MJPEG 9200); 8100, 8110-8130 and 9100-9130 are never used.
4. The Mac screen must be unlocked: `xcodebuild test-without-building` stalls on a locked console session.
5. MCP: `list_simulators`, `boot_simulator` (optional `slim`), `shutdown_simulator`, `slim_simulator`, `unslim_simulator`, `simulator_memory`; then `start_bridge` on the `sim-<udid>` device.
