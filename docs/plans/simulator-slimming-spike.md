# Simulator slimming: spike findings (slices 1-3)

Date: 2026-11-22. Clean-room: only public `xcrun simctl`, `launchctl` (run inside the simulator with `simctl spawn`), `ps` and `footprint(1)`, plus our own measurements. Host: macOS 27.2.1 Mac mini, 24 GB RAM, Xcode simulator runtime **iOS 27.2 (24B8117)** only. No other iOS runtime is installed, so every number below is for iOS 27 (`runtimes` key `"27"` in the profile; other majors fall back to the nearest lower key, and iOS 26 or older reports "no profile").

All experiments ran on simulators we created (`pl-slim-test-1`, and a `simctl clone` of it); user simulators were not touched. Both test simulators were deleted afterwards.

## 1. How to measure

`ps` RSS is **not usable**: a stock iOS 27 simulator sums to 22-26 GB of RSS on a 24 GB host (shared dyld-cache pages are counted per process, and idle processes get compressed, so the user's long-running simulator showed 2.2 GB RSS but 12.5 GB footprint). `footprint(1)` works without sudo for same-user processes and reports `phys_footprint` (dirty + compressed). It accepts many pids at once and prints `Summary Footprint: N B`; with one pid there is no summary line, only the per-process line.

Measurement used by `internal/iossim/memory.go`:

```
ps -axo pid=,ppid=,rss=,command=          # find launchd_sim whose command contains /<udid>/, walk children
footprint -f bytes --noCategories <pid...> # ~0.9 s for 300 pids
```

Footprint is the headline number (`footprintMB`); `rssMB` and `procCount` are reported next to it. Footprint needs ~30-60 s to settle after boot (widget and poster extensions come and go); values below are readings taken after >=45 s, three samples 5-10 s apart (they agreed within 1 MB once settled).

## 2. Stock baselines (iOS 27.2, iPhone 17)

| Situation | Footprint | Processes |
|---|---:|---:|
| Fresh `simctl create`, first boot, settled ~3 min | 13,760 MB | 309 |
| Clone, second boot, settled, run 1 | 10,552 MB | 237 |
| Same clone, later boots (4 readings) | 10,050 / 10,041 / 10,059 / 9,651 MB | 221 |
| User's long-running booted simulator (read only) | 12,510 MB | ~290 |

Stock reference used for ratios: **10,050 MB** (median of the settled boots of one simulator; boot-to-boot noise is about 4 percent). Per-label breakdown (185 labeled running processes) attributes about 7 GB; the remaining ~3 GB are app and extension processes that are not launchd labels (about 25 lock-screen poster extensions of ~70-110 MB each spawned by PosterBoard, widget extensions spawned by chronod, Safari/Messages extensions).

## 3. Mechanism questions

Domain: services live in `user/<uid>` (uid is 501 inside the simulator, `launchctl manageruid`); `launchctl list` shows 402 labels (185 running); the `system` domain only has 7 boot-critical services (logd, diagnosticd, notifyd, containermanagerd.system, ...), which are on the keep list.

| Question | Result | Evidence |
|---|---|---|
| `launchctl disable user/501/<label>` + `bootout` on a booted sim | Works, rc 0, process gone within ~1 s | `simctl spawn <udid> launchctl disable user/501/com.apple.siriinferenced` then `bootout` |
| Does `disable` persist across shutdown/boot? | **Yes.** `print-disabled user/501` still shows `disabled`, label absent from `list` after the reboot | persisted in `/private/var/tmp/com.apple.CoreSimulator.SimDevice.<udid>/disabled.plist` (host side, plain plist `label => true/false`) |
| ...across `simctl erase`? | **Yes** (that directory is not erased) | disabled `siriinferenced` + `weatherd`, erase, boot: both still disabled |
| ...across `simctl clone`? | **No.** The clone starts with an empty disabled set | clone booted, `print-disabled` empty |
| ...across `simctl delete`? | The state directory can be left behind in `/private/var/tmp`; harmless, we removed ours | |
| Do booted-out services respawn? | No. Checked 75 s after an aggressive apply and again after reboot: none of the recorded labels is running | `slim status` `stale` list stayed empty |
| Does disabling things break boot? | **Yes for one label**: disabling `com.apple.nanoregistryd` makes `simctl bootstatus -b` hang forever (bisected: 110 labels, then 20, then 1). It is on the hard keep list. All other candidates boot normally (2-5 s `bootstatus`). | bisect via offline `disabled.plist` edit + boot |
| Offline alternative: edit `disabled.plist` while shut down | Works (shutdown, rewrite plist, boot: labels disabled / enabled as written); used for bisecting | |

Not verified: whether `/private/var/tmp/...SimDevice.<udid>/disabled.plist` survives a **host reboot**. We could not reboot the host. The engine therefore keeps its own sidecar record and re-applies the recorded profile after every boot it performs (and `slim status` flags `stale` labels), so a lost disabled state degrades to "re-run slim", never to a broken simulator.

### Decision

Primary mechanism: `simctl spawn <udid> launchctl disable user/<uid>/<label>` followed by `bootout` (live, no reboot, parallel, ~9 s for 160 labels, state persists). `unslim` = `enable` every recorded label, shut down, boot. SpringBoard relaunches a few system **apps** on every boot (Spotlight, Calendar, the widget renderer) that are not daemons, so profiles also list `apps` that are terminated with `simctl terminate` after each boot (the engine re-applies the recorded profile after a boot it performs).

Offline `disabled.plist` editing is **not implemented**: it depends on a private path under `/private/var/tmp`, needs the simulator shut down, and adds nothing the live mechanism does not already persist. It remains documented here as a fallback if Apple changes launchctl behaviour.

## 4. Results

| Profile | Footprint (live apply, 60 s settle) | After reboot (persisted) | vs stock 10,050 MB | Target |
|---|---:|---:|---:|---|
| stock | 10,050 MB, 221 procs | | 100% | |
| minimal-safe (116 labels) | 5,254 MB, 134 procs | 5,185 MB, 130 procs | **52.3% live / 51.6% rebooted** | <= 55%: met |
| aggressive (165 labels) | 3,015 MB, 98 procs | 2,623 MB, 83 procs | **30.0% live / 26.1% rebooted** | <= 40%: met (stretch 25%: just missed) |

Other readings: first minimal-safe draft (110 labels, no mail/peopled/apps) was 5,719 MB = 56.9 percent, which is why the Mail and Safari sync agents, `peopled`, `passwordbreachd` and the Calendar app were added to minimal-safe. Unslim returned to 10,236 MB / 226 procs with the same launchd label set as stock (only a transient `AMPIDService` and a oneshot id differ) and `print-disabled` empty. The slim sidecar recorded `baselineMB` 10,238 and `slimMB` 3,015 for the aggressive run. The WDA runner, Safari and Settings add ~1.4 GB on top of the idle numbers (6.6 GB measured aggressive with WDA + Safari + Settings open).

The competitor's "about 4x" claim is not reproduced here for minimal-safe (about 1.9x); aggressive reaches about 3.8x after reboot.

## 5. Smoke tests on slimmed simulators (dev server on :8701, own temp home)

| Check | minimal-safe | aggressive |
|---|---|---|
| Settings launches and renders | pass (screenshot) | pass |
| Safari loads https://example.com (`simctl openurl`) | pass (UI tree shows "Example Domain") | pass |
| Start WDA, `pocketlab tree` | pass | pass |
| Typing into the Safari URL bar (`pocketlab tap` + `type`) | pass (field shows the text) | pass |
| `simctl push` to an app with notification authorization (Messages, Settings, Wallet, Clock) | pass ("Notification sent") | pass |
| `simctl location set` / `clear` | exit 0 (not visually verified) | exit 0 |
| Screenshot through the API | pass | pass |
| Unslim restores the service set after reboot | pass (see above) | pass |

Notes: `simctl push` to Safari/Calendar/Reminders/Health/News fails with "Source is not authorized" (those apps have no notification permission), unrelated to slimming. Starting WDA from the supervisor on this machine collided with the user's own Pocketlab WDA on port 8200 (shared host network), so WDA was started with explicit `wdaPort`/`mjpegPort`; after `slim` the supervisor's automatic restart attempt hit the same port conflict (an environment artifact, not slimming).

Visible side effects: home-screen widgets render as blank white tiles (chronod disabled); with `lockscreen` (PosterBoard) disabled the home screen wallpaper still renders but wallpaper editing will not work; the first boot after a disable may log missing-service errors.

## 6. Label table, iOS 27.2 (stock clone, settled; footprint per process)

Group is the profile group that disables the label; KEEP = hard keep list (never disabled); `-` = not touched by any profile. 167 labeled running processes, 6.9 GB; 4.1 GB of that is assigned to a group. RSS is omitted because it over-counts shared pages (see section 1); `pocketlab sim slim <udid> --discover` prints both.

| Footprint MB | Label | Group |
|---:|---|---|
| 192.6 | `com.apple.SpringBoard` | KEEP |
| 126.2 | `app com.apple.mobilecal` | widgets (terminated app) |
| 122.6 | `app com.apple.Spotlight` | spotlight (terminated app) |
| 120.3 | `com.apple.PosterBoard` | lockscreen |
| 104.8 | `com.apple.intelligenceflowd` | intelligence |
| 104.4 | `com.apple.sharingd` | - |
| 104.3 | `com.apple.email.maild` | mail |
| 99.9 | `com.apple.calaccessd` | pim |
| 96.6 | `com.apple.ScreenTimeAgent` | screentime |
| 96.4 | `com.apple.icloudmailagent` | mail |
| 92.5 | `com.apple.intelligencecontextd` | intelligence |
| 83.1 | `com.apple.assistantd` | siri |
| 82.5 | `app com.apple.chrono.WidgetRenderer-Default` | widgets (terminated app) |
| 82.2 | `com.apple.siriactionsd` | siri |
| 81.9 | `com.apple.homed` | homekit |
| 80.6 | `com.apple.chronod` | widgets |
| 77.3 | `com.apple.telephonyutilities.callservicesd` | comms |
| 74.9 | `com.apple.siriinferenced` | siri |
| 73.0 | `com.apple.remindd` | pim |
| 72.2 | `com.apple.InputUI` | KEEP |
| 71.6 | `com.apple.routined` | location |
| 70.1 | `com.apple.suggestd` | intelligence |
| 69.9 | `com.apple.AccessibilityUIServer` | KEEP |
| 68.5 | `com.apple.donotdisturbd` | - |
| 66.7 | `com.apple.passd` | homekit |
| 65.2 | `com.apple.NPKCompanionAgent` | companion |
| 64.6 | `com.apple.nanotimekitcompaniond` | companion |
| 63.7 | `com.apple.liveactivitiesd` | widgets |
| 63.1 | `com.apple.usernotificationsd` | - |
| 61.0 | `com.apple.managedconfiguration.profiled` | - |
| 60.7 | `com.apple.AuthenticationServicesCore.AuthenticationServicesAgent` | - |
| 58.8 | `com.apple.healthd` | healthkit |
| 56.4 | `com.apple.generativeexperiencesd` | intelligence |
| 55.5 | `com.apple.WebBookmarks.webbookmarksd` | icloud |
| 55.4 | `com.apple.FamilyControlsAgent` | screentime |
| 54.9 | `com.apple.financed` | homekit |
| 54.7 | `com.apple.activitysharingd` | healthkit |
| 54.7 | `com.apple.navd` | location |
| 54.6 | `com.apple.sociallayerd` | comms |
| 54.0 | `com.apple.searchd` | spotlight |
| 53.7 | `com.apple.dataaccess.dataaccessd` | pim |
| 53.2 | `com.apple.corespeechd` | siri |
| 52.5 | `com.apple.biomed` | analytics |
| 52.3 | `com.apple.appleaccountd` | - |
| 52.2 | `com.apple.sirittsd` | siri |
| 52.0 | `com.apple.mediaremoted` | - |
| 51.9 | `com.apple.fitnesscoachingd` | healthkit |
| 51.8 | `com.apple.iconservices.iconservicesagent` | KEEP |
| 51.3 | `com.apple.ScreenTimeSettingsAgent` | screentime |
| 51.3 | `com.apple.SafariBookmarksSyncAgent` | mail |
| 51.0 | `com.apple.peopled` | intelligence |
| 50.7 | `com.apple.fitcore` | healthkit |
| 48.9 | `com.apple.mobiletimerd` | - |
| 48.0 | `com.apple.photoanalysisd` | photos |
| 47.1 | `com.apple.weatherd` | news-weather |
| 47.1 | `com.apple.spotlightknowledged.updater` | spotlight |
| 45.6 | `com.apple.diagnosticextensionsd` | analytics |
| 45.2 | `com.apple.accessibility.axassetsd` | KEEP |
| 44.5 | `com.apple.companionappd` | companion |
| 43.7 | `com.apple.addressbooksyncd` | companion |
| 43.1 | `com.apple.bulletindistributord` | companion |
| 42.9 | `com.apple.textunderstandingd` | intelligence |
| 42.2 | `com.apple.avatarsd` | photos |
| 41.4 | `com.apple.modelcatalogd` | intelligence |
| 41.3 | `com.apple.mediaanalysisd` | photos |
| 40.3 | `com.apple.linkd` | - |
| 39.8 | `com.apple.itunesstored` | - |
| 39.8 | `com.apple.ap.promotedcontentd` | analytics |
| 39.5 | `com.apple.Safari.passwordbreachd` | analytics |
| 39.0 | `com.apple.deviceaccessd` | - |
| 38.5 | `com.apple.identityservicesd` | KEEP |
| 38.5 | `com.apple.gamed` | gamecenter |
| 38.5 | `com.apple.sleepd` | healthkit |
| 38.4 | `com.apple.fileindexerd` | spotlight |
| 38.0 | `com.apple.amsengagementd` | analytics |
| 37.7 | `com.apple.backboardd` | KEEP |
| 37.7 | `com.apple.findmy.findmylocated` | location |
| 37.3 | `com.apple.fitnessintelligenced` | healthkit |
| 37.2 | `com.apple.voicebankingd` | siri |
| 37.0 | `com.apple.ManagedSettingsAgent` | screentime |
| 36.0 | `com.apple.FileProvider` | - |
| 36.0 | `com.apple.mobileassetd` | KEEP |
| 35.8 | `com.apple.coredevice.dtdeviceinfod` | KEEP |
| 34.9 | `com.apple.dmd` | - |
| 34.5 | `com.apple.contactsd` | pim |
| 34.0 | `com.apple.appstored` | - |
| 33.9 | `com.apple.familycircled` | screentime |
| 33.7 | `com.apple.communicationtrustd` | - |
| 33.6 | `com.apple.jetpackassetd` | - |
| 33.5 | `com.apple.testmanagerd` | KEEP |
| 33.3 | `com.apple.brook.brookcompaniond` | companion |
| 33.1 | `com.apple.siri.context.service` | siri |
| 33.0 | `com.apple.rapportd` | - |
| 33.0 | `com.apple.syncdefaultsd` | icloud |
| 32.9 | `com.apple.nanoregistryd` | KEEP |
| 32.8 | `com.apple.assistant_cdmd` | siri |
| 32.4 | `com.apple.assetsd` | - |
| 32.1 | `com.apple.CoreSimulator.bridge` | KEEP |
| 31.9 | `com.apple.homeeventsd` | homekit |
| 31.7 | `com.apple.translationd` | - |
| 31.7 | `com.apple.managedassetsd` | - |
| 31.5 | `com.apple.locationd` | KEEP |
| 31.2 | `com.apple.eligibilityd` | - |
| 31.0 | `com.apple.dasd` | - |
| 30.9 | `com.apple.carkitd` | location |
| 30.5 | `com.apple.ind` | - |
| 30.3 | `com.apple.cdpd` | icloud |
| 30.3 | `com.apple.accountsd` | KEEP |
| 30.2 | `com.apple.wcd` | companion |
| 30.2 | `com.apple.DeviceConfigurationAgent` | - |
| 29.8 | `com.apple.itunescloudd` | icloud |
| 29.3 | `com.apple.pasteboard.pasted` | KEEP |
| 28.9 | `com.apple.localizationswitcherd` | - |
| 28.8 | `com.apple.triald` | analytics |
| 28.4 | `com.apple.appprotectiond` | - |
| 28.1 | `com.apple.ap.adprivacyd` | analytics |
| 27.9 | `com.apple.ClipServices.clipserviced` | - |
| 27.7 | `com.apple.UsageTrackingAgent` | analytics |
| 26.8 | `com.apple.devicecheckd` | - |
| 25.9 | `com.apple.cloudd` | icloud |
| 25.7 | `com.apple.featureaccessd` | - |
| 25.7 | `com.apple.biomesyncd` | analytics |
| 25.0 | `com.apple.installcoordinationd` | - |
| 24.9 | `com.apple.amsaccountsd` | - |
| 24.7 | `com.apple.UserEventAgent-System` | KEEP |
| 24.6 | `com.apple.securityd` | KEEP |
| 24.6 | `com.apple.eventkitsyncd` | companion |
| 24.3 | `com.apple.modelmanagerd` | intelligence |
| 24.1 | `com.apple.tccd` | KEEP |
| 23.9 | `com.apple.parsecd` | analytics |
| 23.7 | `com.apple.activityawardsd` | healthkit |
| 23.6 | `com.apple.systemstatusd` | - |
| 23.1 | `com.apple.siriknowledged` | siri |
| 22.5 | `com.apple.geod` | - |
| 22.0 | `com.apple.runningboardd` | KEEP |
| 20.0 | `com.apple.GameController.gamecontrollerd` | - |
| 19.3 | `com.apple.akd` | - |
| 17.4 | `com.apple.assetsubscriptiond` | - |
| 16.5 | `com.apple.pluginkit.pkd` | KEEP |
| 16.5 | `com.apple.purplebuddy.budd` | KEEP |
| 16.4 | `com.apple.replicatord` | - |
| 15.9 | `com.apple.nanoprefsyncd.2` | companion |
| 15.7 | `com.apple.nsurlsessiond` | KEEP |
| 13.9 | `com.apple.apsd` | KEEP |
| 13.5 | `com.apple.lsd` | KEEP |
| 13.0 | `com.apple.medialibraryd` | - |
| 12.9 | `com.apple.swcd` | - |
| 12.6 | `com.apple.FileCoordination` | - |
| 12.5 | `com.apple.trustd` | KEEP |
| 12.2 | `com.apple.appconduitd` | - |
| 11.4 | `com.apple.followupd` | - |
| 11.3 | `com.apple.mobile.installd` | KEEP |
| 10.2 | `com.apple.MobileInstallationHelperService` | KEEP |
| 9.5 | `com.apple.fontservicesd` | KEEP |
| 9.4 | `com.apple.containermanagerd` | KEEP |
| 9.2 | `com.apple.countryd` | - |
| 8.2 | `com.apple.datamigrator` | KEEP |
| 8.1 | `com.apple.ids_simd` | KEEP |
| 7.4 | `com.apple.revisiond` | - |
| 7.0 | `com.apple.AssetCacheLocatorService` | - |
| 7.0 | `com.apple.nanoregistrylaunchd` | companion |
| 7.0 | `com.apple.configd_sim` | KEEP |
| 6.8 | `com.apple.cfprefsd.xpc.daemon` | KEEP |
| 6.1 | `com.apple.distnoted.xpc.daemon` | KEEP |
| 2.4 | `com.apple.syslogd` | KEEP |
| 2.2 | `com.apple.aslmanager` | KEEP |

Labels assigned to groups that were not running in the stock boot (loaded but idle, or on-demand) are listed in `internal/iossim/slim/profiles.json`.

## 7. Reproduce

```
pocketlab sim slim <udid> --discover        # label inventory with footprint, group, keep flag (runs locally)
pocketlab sim slim <udid> --dry-run
pocketlab sim slim <udid> --profile aggressive
pocketlab sim mem <udid>
pocketlab sim unslim <udid>
```
