# iOS setup / iOS kurulumu

PhoneForge controls real iPhones through its own signed build of
[appium/WebDriverAgent](https://github.com/appium/WebDriverAgent) (BSD licence).

## English

1. Install Xcode, open **Xcode > Settings > Accounts** and sign in with your Apple ID (free or paid).
   Create an "Apple Development" certificate if none exists.
2. On the iPhone: **Settings > Privacy & Security > Developer Mode > On** (the phone reboots), then confirm.
3. Connect the phone by cable, unlock it, tap **Trust**. Let Xcode finish "Preparing device" once.
4. Build and sign WDA (does not touch the phone):
   `scripts/build-wda.sh`
   Overrides: `PF_TEAM_ID`, `PF_BUNDLE_PREFIX` (default `com.phoneforge`).
5. Install on the phone (explicit opt-in):
   `scripts/build-wda.sh --install <udid>`
6. Trust the developer: **Settings > General > VPN & Device Management > your Apple Development profile > Trust**.
7. Open the PhoneForge UI, select the device, click **Install helper** (if shown) and then **Start WDA**.

Free accounts: profiles expire after 7 days (rebuild and reinstall) and the number of app ids per week is limited.

## Turkce

1. Xcode'u kurun, **Xcode > Settings > Accounts** bolumunden Apple ID ile giris yapin (ucretsiz veya ucretli). "Apple Development" sertifikasi yoksa olusturun.
2. iPhone'da: **Ayarlar > Gizlilik ve Guvenlik > Gelistirici Modu > Acik** (telefon yeniden baslar), onaylayin.
3. Telefonu kabloyla baglayin, kilidini acin, **Guven** deyin. Xcode'un "Preparing device" islemini bir kez bitirmesine izin verin.
4. WDA'yi derleyip imzalayin (telefona dokunmaz): `scripts/build-wda.sh`
   Degiskenler: `PF_TEAM_ID`, `PF_BUNDLE_PREFIX` (varsayilan `com.phoneforge`).
5. Telefona kurmak icin (acik onay): `scripts/build-wda.sh --install <udid>`
6. Gelistiriciye guvenin: **Ayarlar > Genel > VPN ve Aygit Yonetimi > Apple Development profiliniz > Guven**.
7. PhoneForge arayuzunu acin, cihazi secin ve **Start bridge** tusuna basin.

Ucretsiz hesaplarda profil 7 gun sonra dolar (yeniden derleyip kurun); haftalik app id sayisi sinirlidir.

## iOS Simulator

Simulators need no signing or phone. Build the simulator WDA once:
`scripts/build-wda.sh --sim` (output: `~/.phoneforge/wda/build-sim`).

1. UI: **Integrations > iOS Simulators > Boot**, then **Start WDA**. Or REST:
   `POST /api/v1/simulators/{udid}/boot`, then `POST /api/v1/simulators/{udid}/wda/start`.
2. The booted simulator appears in the device list as `sim-<udid>` and works like an iPhone
   (screenshot, UI tree, tap, swipe, type, Home, app launch, install, open URL, MJPEG live view).
   Without WDA only screenshots, app list/launch/stop/install, open URL, clipboard and logs work (via `simctl`).
3. WDA ports are allocated per simulator from 8200 (MJPEG 9200); 8100, 8110-8130 and 9100-9130 are never used.
4. The Mac screen must be unlocked: `xcodebuild test-without-building` stalls on a locked console session.
5. MCP: `list_simulators`, `boot_simulator`, `shutdown_simulator`; then `start_bridge` on the `sim-<udid>` device.
