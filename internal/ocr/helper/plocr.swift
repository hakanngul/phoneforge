// plocr: reads a PNG (path arg, or stdin when no path / "-") and prints a JSON
// array of {text,x,y,w,h,confidence} in top-left-origin pixel coordinates.
// Flags: --words (word-level boxes instead of lines), --fast (fast recognition).
import Foundation
import Vision
import ImageIO
import CoreGraphics

struct Item: Codable {
    let text: String
    let x: Int
    let y: Int
    let w: Int
    let h: Int
    let confidence: Double
}

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write((msg + "\n").data(using: .utf8)!)
    exit(1)
}

var words = false
var fast = false
var path: String? = nil
for a in CommandLine.arguments.dropFirst() {
    switch a {
    case "--words": words = true
    case "--fast": fast = true
    case "-": path = nil
    default: path = a
    }
}

let data: Data
if let p = path {
    guard let d = FileManager.default.contents(atPath: p) else { fail("cannot read \(p)") }
    data = d
} else {
    data = FileHandle.standardInput.readDataToEndOfFile()
}
if data.isEmpty { fail("no image data") }

guard let src = CGImageSourceCreateWithData(data as CFData, nil),
      let img = CGImageSourceCreateImageAtIndex(src, 0, nil) else { fail("cannot decode image") }

let W = Double(img.width)
let H = Double(img.height)

let req = VNRecognizeTextRequest()
req.recognitionLevel = fast ? .fast : .accurate
req.usesLanguageCorrection = true
req.recognitionLanguages = ["en-US", "tr-TR"]
if #available(macOS 13.0, *) {
    req.automaticallyDetectsLanguage = false
}
// Fall back to defaults if a requested language is unsupported.
if let supported = try? req.supportedRecognitionLanguages() {
    let want = req.recognitionLanguages.filter { supported.contains($0) }
    if !want.isEmpty { req.recognitionLanguages = want }
}

let handler = VNImageRequestHandler(cgImage: img, options: [:])
do { try handler.perform([req]) } catch { fail("vision error: \(error)") }

func convert(_ b: CGRect) -> (Int, Int, Int, Int) {
    let x = Int((b.origin.x * W).rounded())
    let w = Int((b.size.width * W).rounded())
    let h = Int((b.size.height * H).rounded())
    let y = Int(((1.0 - b.origin.y - b.size.height) * H).rounded())
    return (x, y, w, h)
}

var out: [Item] = []
for obs in req.results ?? [] {
    guard let cand = obs.topCandidates(1).first else { continue }
    let conf = Double(cand.confidence)
    if words {
        let s = cand.string
        var idx = s.startIndex
        while idx < s.endIndex {
            while idx < s.endIndex && s[idx].isWhitespace { idx = s.index(after: idx) }
            if idx >= s.endIndex { break }
            var end = idx
            while end < s.endIndex && !s[end].isWhitespace { end = s.index(after: end) }
            let range = idx..<end
            if let box = try? cand.boundingBox(for: range) {
                let (x, y, w, h) = convert(box.boundingBox)
                out.append(Item(text: String(s[range]), x: x, y: y, w: w, h: h, confidence: conf))
            }
            idx = end
        }
    } else {
        let (x, y, w, h) = convert(obs.boundingBox)
        out.append(Item(text: cand.string, x: x, y: y, w: w, h: h, confidence: conf))
    }
}

let enc = JSONEncoder()
let json = try! enc.encode(out)
FileHandle.standardOutput.write(json)
FileHandle.standardOutput.write("\n".data(using: .utf8)!)
