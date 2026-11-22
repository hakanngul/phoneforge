// plcapture: macOS native USB screen capture of iOS devices (the QuickTime
// mechanism) via CoreMediaIO + AVCaptureSession.
//
//   plcapture list
//   plcapture stream --id <uniqueID> [--fps 60] [--scale 0.5] [--quality 0.6]
//
// stream writes frames to stdout as [uint32 big-endian length][JPEG bytes].
// Status/errors go to stderr as one JSON object per line. Exit codes:
//   10 camera permission denied/restricted   11 device not found
//   12 capture session failed / device busy  13 no frames arrived (locked/untrusted)
import AVFoundation
import CoreMediaIO
import CoreImage
import Foundation

func logEvent(_ event: String, _ msg: String = "") {
    let o: [String: String] = ["event": event, "message": msg]
    if let d = try? JSONSerialization.data(withJSONObject: o), let s = String(data: d, encoding: .utf8) {
        FileHandle.standardError.write((s + "\n").data(using: .utf8)!)
    }
}

func die(_ code: Int32, _ event: String, _ msg: String) -> Never {
    logEvent(event, msg)
    exit(code)
}

func enableScreenCaptureDevices() {
    var addr = CMIOObjectPropertyAddress(
        mSelector: CMIOObjectPropertySelector(kCMIOHardwarePropertyAllowScreenCaptureDevices),
        mScope: CMIOObjectPropertyScope(kCMIOObjectPropertyScopeGlobal),
        mElement: CMIOObjectPropertyElement(kCMIOObjectPropertyElementMain))
    var allow: UInt32 = 1
    CMIOObjectSetPropertyData(CMIOObjectID(kCMIOObjectSystemObject), &addr, 0, nil,
                              UInt32(MemoryLayout<UInt32>.size), &allow)
}

func authName() -> String {
    switch AVCaptureDevice.authorizationStatus(for: .video) {
    case .authorized: return "authorized"
    case .denied: return "denied"
    case .restricted: return "restricted"
    case .notDetermined: return "notDetermined"
    @unknown default: return "unknown"
    }
}

func discover() -> [AVCaptureDevice] {
    let ds = AVCaptureDevice.DiscoverySession(deviceTypes: [.external], mediaType: nil, position: .unspecified)
    var out = ds.devices
    for d in AVCaptureDevice.DiscoverySession(deviceTypes: [.external], mediaType: .muxed, position: .unspecified).devices
    where !out.contains(where: { $0.uniqueID == d.uniqueID }) { out.append(d) }
    return out.filter { $0.modelID == "iOS Device" || $0.manufacturer == "Apple Inc." && $0.deviceType == .external }
}

// Devices appear asynchronously after enabling; poll briefly.
func discoverWaiting(_ seconds: Double, until pred: (([AVCaptureDevice]) -> Bool)? = nil) -> [AVCaptureDevice] {
    let end = Date().addingTimeInterval(seconds)
    var devs = discover()
    while Date() < end && !(pred?(devs) ?? !devs.isEmpty) {
        RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        devs = discover()
    }
    return devs
}

func argValue(_ name: String) -> String? {
    let a = CommandLine.arguments
    if let i = a.firstIndex(of: name), i + 1 < a.count { return a[i + 1] }
    return nil
}

func normalize(_ s: String) -> String { s.replacingOccurrences(of: "-", with: "").lowercased() }

final class Streamer: NSObject, AVCaptureVideoDataOutputSampleBufferDelegate {
    let ctx = CIContext(options: [.useSoftwareRenderer: false])
    let scale: CGFloat
    let quality: CGFloat
    let minInterval: Double
    var last = 0.0
    var count = 0
    let space = CGColorSpaceCreateDeviceRGB()
    init(scale: Double, quality: Double, fps: Double) {
        self.scale = CGFloat(scale); self.quality = CGFloat(quality)
        self.minInterval = fps > 0 ? 1.0 / fps : 0
    }
    func captureOutput(_ output: AVCaptureOutput, didOutput sb: CMSampleBuffer, from c: AVCaptureConnection) {
        let now = CFAbsoluteTimeGetCurrent()
        if now - last < minInterval * 0.9 { return }
        guard let pb = CMSampleBufferGetImageBuffer(sb) else { return }
        last = now
        var img = CIImage(cvPixelBuffer: pb)
        if scale < 0.999 {
            img = img.transformed(by: CGAffineTransform(scaleX: scale, y: scale))
        }
        guard let data = ctx.jpegRepresentation(of: img, colorSpace: space,
              options: [kCGImageDestinationLossyCompressionQuality as CIImageRepresentationOption: quality]) else { return }
        var len = UInt32(data.count).bigEndian
        var hdr = Data(bytes: &len, count: 4)
        hdr.append(data)
        hdr.withUnsafeBytes { p in
            var off = 0
            while off < p.count {
                let n = write(1, p.baseAddress! + off, p.count - off)
                if n <= 0 { exit(0) } // reader gone
                off += n
            }
        }
        count += 1
        if count == 1 { logEvent("first_frame", "\(Int(CVPixelBufferGetWidth(pb)))x\(Int(CVPixelBufferGetHeight(pb)))") }
    }
}

enableScreenCaptureDevices()
signal(SIGPIPE, SIG_IGN)
let cmd = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ""

switch cmd {
case "list":
    let devs = discoverWaiting(Double(argValue("--wait") ?? "2") ?? 2)
    let arr: [[String: Any]] = devs.map {
        ["id": $0.uniqueID, "name": $0.localizedName, "model": $0.modelID,
         "manufacturer": $0.manufacturer, "connected": $0.isConnected, "inUseByAnotherApplication": $0.isInUseByAnotherApplication,
         "suspended": $0.isSuspended]
    }
    let out: [String: Any] = ["authorization": authName(), "devices": arr]
    let d = try! JSONSerialization.data(withJSONObject: out, options: [.sortedKeys])
    print(String(data: d, encoding: .utf8)!)
case "stream":
    let fps = Double(argValue("--fps") ?? "60") ?? 60
    let scale = Double(argValue("--scale") ?? "0.5") ?? 0.5
    let quality = Double(argValue("--quality") ?? "0.6") ?? 0.6
    let wantID = argValue("--id") ?? argValue("--udid")
    guard let want = wantID else { die(2, "usage", "stream needs --id") }

    var status = AVCaptureDevice.authorizationStatus(for: .video)
    if status == .notDetermined {
        logEvent("tcc_prompt", "requesting camera access; approve the macOS prompt")
        let sem = DispatchSemaphore(value: 0)
        AVCaptureDevice.requestAccess(for: .video) { _ in sem.signal() }
        while sem.wait(timeout: .now() + 0.1) == .timedOut { RunLoop.current.run(until: Date().addingTimeInterval(0.05)) }
        status = AVCaptureDevice.authorizationStatus(for: .video)
    }
    if status == .denied || status == .restricted {
        die(10, "tcc_denied", "camera permission \(authName()): allow the app that runs Pocketlab in System Settings > Privacy & Security > Camera")
    }
    if status == .notDetermined { die(10, "tcc_undetermined", "camera permission was not granted") }

    let devs = discoverWaiting(5) { $0.contains { normalize($0.uniqueID) == normalize(want) } }
    guard let dev = devs.first(where: { normalize($0.uniqueID) == normalize(want) }) else {
        die(11, "device_not_found", "no capture device \(want) (is the iPhone unlocked and trusted?)")
    }
    if dev.isInUseByAnotherApplication { logEvent("in_use", "device is in use by another application") }

    let session = AVCaptureSession()
    guard let input = try? AVCaptureDeviceInput(device: dev), session.canAddInput(input) else {
        die(12, "session_failed", "cannot open capture input (device busy, e.g. QuickTime?)")
    }
    session.addInput(input)
    let out = AVCaptureVideoDataOutput()
    out.alwaysDiscardsLateVideoFrames = true
    out.videoSettings = [kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA]
    let streamer = Streamer(scale: scale, quality: quality, fps: fps)
    out.setSampleBufferDelegate(streamer, queue: DispatchQueue(label: "plcapture.frames"))
    guard session.canAddOutput(out) else { die(12, "session_failed", "cannot add video output") }
    session.addOutput(out)
    NotificationCenter.default.addObserver(forName: .AVCaptureSessionRuntimeError, object: session, queue: nil) { n in
        die(12, "session_failed", "runtime error: \(n.userInfo?[AVCaptureSessionErrorKey] ?? "unknown")")
    }
    NotificationCenter.default.addObserver(forName: .AVCaptureDeviceWasDisconnected, object: nil, queue: nil) { _ in
        die(11, "device_disconnected", "device disconnected")
    }
    session.startRunning()
    logEvent("started", dev.localizedName)
    DispatchQueue.main.asyncAfter(deadline: .now() + 8) {
        if streamer.count == 0 { die(13, "no_frames", "no frames in 8s: unlock the iPhone and tap Trust, or another app holds it") }
    }
    RunLoop.main.run()
default:
    die(2, "usage", "usage: plcapture list | stream --id ID [--fps N --scale F --quality F]")
}
