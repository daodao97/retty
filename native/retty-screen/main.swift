// retty-screen shows this Mac's screen to a paired device and takes its
// input. The desktop app builds without cgo, so ScreenCaptureKit, ImageIO and
// CGEvent live in this small helper, run as a child of the app; macOS counts
// its screen recording and accessibility access as the app's own.
//
//   retty-screen check     prints {"screen":bool,"input":bool}
//   retty-screen request   asks for both permissions, then prints as check
//   retty-screen capture <maxWidth> <quality 1-100> <fps>
//       streams the main display as [uint32 big-endian length][JPEG], a frame
//       only when the picture changed. A "frame" line on stdin sends the last
//       frame again, so a viewer who joins sees the screen at once.
//   retty-screen input
//       reads one event per line of stdin, with x and y between 0 and 1 of
//       the main display:
//         move x y | down x y button | up x y button | drag x y button
//         click x y button count
//         scroll dx dy | text base64 | key keycode flags
//       button is 0 left, 1 right; flags are CGEventFlags bits.
//   retty-screen vdisplay <width> <height>
//       adds a software display of width × height points, drawn at twice
//       that, for a Mac with none of its own, as when its lid is shut; it
//       stays while stdin stays open, and prints its display ID once.
//   retty-screen displays
//       prints how many real displays are online, its own not counted.
//
// A fatal problem prints one line to stderr and exits non-zero.

import ApplicationServices
import CoreGraphics
import CoreImage
import CoreMedia
import Foundation
import ImageIO
import ScreenCaptureKit

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}

func permissions() -> String {
    "{\"screen\":\(CGPreflightScreenCaptureAccess()),\"input\":\(AXIsProcessTrusted())}"
}

let args = CommandLine.arguments
guard args.count >= 2 else { fail("usage: retty-screen check|request|capture|input") }

switch args[1] {
case "check":
    print(permissions())
    exit(0)
case "request":
    if !CGPreflightScreenCaptureAccess() {
        CGRequestScreenCaptureAccess()
    }
    let prompt = kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String
    _ = AXIsProcessTrustedWithOptions([prompt: true] as CFDictionary)
    print(permissions())
    exit(0)
case "capture":
    guard args.count == 5, let maxWidth = Int(args[2]), let quality = Int(args[3]), let fps = Int(args[4]),
          maxWidth > 0, (1...100).contains(quality), (1...60).contains(fps) else {
        fail("usage: retty-screen capture <maxWidth> <quality> <fps>")
    }
    Capture.run(maxWidth: maxWidth, quality: Double(quality) / 100, fps: fps)
case "input":
    Input.run()
case "vdisplay":
    guard args.count == 4, let width = UInt32(args[2]), let height = UInt32(args[3]),
          (640...3840).contains(width), (480...2400).contains(height) else {
        fail("usage: retty-screen vdisplay <width> <height>")
    }
    VirtualDisplay.run(width: width, height: height)
case "displays":
    print(VirtualDisplay.realDisplays())
    exit(0)
default:
    fail("unknown command \(args[1])")
}

// MARK: - Capture

final class Capture: NSObject, SCStreamOutput, SCStreamDelegate {
    let quality: Double
    let context = CIContext()
    let colorSpace = CGColorSpace(name: CGColorSpace.sRGB)!
    let out = FileHandle.standardOutput
    let lock = NSLock()
    var last: Data?
    var stream: SCStream?

    init(quality: Double) { self.quality = quality }

    static func run(maxWidth: Int, quality: Double, fps: Int) -> Never {
        if !CGPreflightScreenCaptureAccess() {
            fail("screen recording permission denied")
        }
        let capture = Capture(quality: quality)
        SCShareableContent.getExcludingDesktopWindows(false, onScreenWindowsOnly: true) { content, error in
            guard let content else { fail("no shareable content: \(error?.localizedDescription ?? "unknown")") }
            let main = CGMainDisplayID()
            guard let display = content.displays.first(where: { $0.displayID == main }) ?? content.displays.first else {
                fail("no display")
            }
            let config = SCStreamConfiguration()
            // The display's pixels, at most maxWidth wide, keep its shape.
            let scale = min(1, Double(maxWidth) / Double(display.width))
            config.width = max(2, Int(Double(display.width) * scale))
            config.height = max(2, Int(Double(display.height) * scale))
            config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
            config.pixelFormat = kCVPixelFormatType_32BGRA
            config.showsCursor = true
            config.queueDepth = 3
            let filter = SCContentFilter(display: display, excludingWindows: [])
            let stream = SCStream(filter: filter, configuration: config, delegate: capture)
            do {
                try stream.addStreamOutput(capture, type: .screen, sampleHandlerQueue: DispatchQueue(label: "retty.screen"))
            } catch {
                fail("cannot add output: \(error.localizedDescription)")
            }
            capture.stream = stream
            stream.startCapture { error in
                if let error { fail("cannot start capture: \(error.localizedDescription)") }
            }
        }
        // The app asks for the last frame again when a viewer joins; its
        // closing stdin ends the capture.
        Thread {
            while let line = readLine(strippingNewline: true) {
                if line == "frame" { capture.resend() }
            }
            exit(0)
        }.start()
        dispatchMain()
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sample: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen, sample.isValid,
              let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false) as? [[SCStreamFrameInfo: Any]],
              let raw = attachments.first?[.status] as? Int,
              SCFrameStatus(rawValue: raw) == .complete,
              let pixels = CMSampleBufferGetImageBuffer(sample) else { return }
        let image = CIImage(cvPixelBuffer: pixels)
        let options = [CIImageRepresentationOption(rawValue: kCGImageDestinationLossyCompressionQuality as String): quality]
        guard let jpeg = context.jpegRepresentation(of: image, colorSpace: colorSpace, options: options) else { return }
        write(jpeg, keep: true)
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        fail("capture stopped: \(error.localizedDescription)")
    }

    func resend() {
        lock.lock()
        let frame = last
        lock.unlock()
        if let frame { write(frame, keep: false) }
    }

    func write(_ jpeg: Data, keep: Bool) {
        var length = UInt32(jpeg.count).bigEndian
        let head = Data(bytes: &length, count: 4)
        lock.lock()
        defer { lock.unlock() }
        if keep { last = jpeg }
        // The header and the picture go out in turn, under the lock, rather
        // than the picture being copied behind its header. A closed pipe
        // means the app is gone.
        do {
            try out.write(contentsOf: head)
            try out.write(contentsOf: jpeg)
        } catch { exit(0) }
    }
}

// MARK: - Input

enum Input {
    static func run() -> Never {
        if !AXIsProcessTrusted() {
            fail("accessibility permission denied")
        }
        while let line = readLine(strippingNewline: true) {
            handle(line.split(separator: " ").map(String.init))
        }
        exit(0)
    }

    static func point(_ x: String, _ y: String) -> CGPoint? {
        guard let x = Double(x), let y = Double(y), (0...1).contains(x), (0...1).contains(y) else { return nil }
        let bounds = CGDisplayBounds(CGMainDisplayID())
        return CGPoint(x: bounds.minX + x * bounds.width, y: bounds.minY + y * bounds.height)
    }

    // count is the click count macOS reads to see a double or triple click.
    static func mouse(_ type: CGEventType, _ at: CGPoint, _ button: CGMouseButton, count: Int64 = 1) {
        let event = CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: at, mouseButton: button)
        event?.setIntegerValueField(.mouseEventClickState, value: count)
        event?.post(tap: .cghidEventTap)
    }

    static func handle(_ f: [String]) {
        guard let op = f.first else { return }
        switch op {
        case "move" where f.count == 3:
            if let at = point(f[1], f[2]) { mouse(.mouseMoved, at, .left) }
        case "down" where f.count == 4, "up" where f.count == 4, "drag" where f.count == 4:
            guard let at = point(f[1], f[2]) else { return }
            let right = f[3] == "1"
            let type: CGEventType
            switch op {
            case "down": type = right ? .rightMouseDown : .leftMouseDown
            case "up": type = right ? .rightMouseUp : .leftMouseUp
            default: type = right ? .rightMouseDragged : .leftMouseDragged
            }
            mouse(type, at, right ? .right : .left)
        case "click" where f.count == 5:
            guard let at = point(f[1], f[2]), let count = Int64(f[4]), (1...3).contains(count) else { return }
            let right = f[3] == "1"
            mouse(right ? .rightMouseDown : .leftMouseDown, at, right ? .right : .left, count: count)
            mouse(right ? .rightMouseUp : .leftMouseUp, at, right ? .right : .left, count: count)
        case "scroll" where f.count == 3:
            guard let dx = Int32(f[1]), let dy = Int32(f[2]) else { return }
            CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 2, wheel1: dy, wheel2: dx, wheel3: 0)?.post(tap: .cghidEventTap)
        case "text" where f.count == 2:
            guard let data = Data(base64Encoded: f[1]), let text = String(data: data, encoding: .utf8) else { return }
            type(text)
        case "key" where f.count == 3:
            guard let code = CGKeyCode(f[1]), let flags = UInt64(f[2]) else { return }
            for down in [true, false] {
                let event = CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: down)
                event?.flags = CGEventFlags(rawValue: flags)
                event?.post(tap: .cghidEventTap)
            }
        default:
            break
        }
    }

    // Text goes in as Unicode, not key codes, so any layout and any script
    // types what was sent. A key event carries at most 20 UTF-16 units.
    static func type(_ text: String) {
        let units = Array(text.utf16)
        var i = 0
        while i < units.count {
            var chunk = Array(units[i..<min(i + 20, units.count)])
            for down in [true, false] {
                let event = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: down)
                event?.keyboardSetUnicodeString(stringLength: chunk.count, unicodeString: &chunk)
                event?.post(tap: .cghidEventTap)
            }
            i += 20
        }
    }
}

// MARK: - Virtual display

// held keeps the virtual display, which releasing removes.
var held: CGVirtualDisplay?

enum VirtualDisplay {
    // The display's EDID identity, which tells it from real ones.
    static let vendor: UInt32 = 0x5254 // "RT"
    static let product: UInt32 = 0x0001

    static func realDisplays() -> Int {
        var count: UInt32 = 0
        CGGetOnlineDisplayList(0, nil, &count)
        var ids = [CGDirectDisplayID](repeating: 0, count: Int(count))
        CGGetOnlineDisplayList(count, &ids, &count)
        return ids.prefix(Int(count)).filter {
            !(CGDisplayVendorNumber($0) == vendor && CGDisplayModelNumber($0) == product)
        }.count
    }

    static func run(width: UInt32, height: UInt32) -> Never {
        // The classes are private; a macOS without them fails here rather
        // than crashing on a missing selector.
        guard NSClassFromString("CGVirtualDisplay") != nil else {
            fail("virtual displays are not available on this macOS")
        }
        let descriptor = CGVirtualDisplayDescriptor()
        descriptor.queue = DispatchQueue(label: "retty.vdisplay")
        descriptor.name = "Retty"
        descriptor.maxPixelsWide = width * 2
        descriptor.maxPixelsHigh = height * 2
        // A 24 inch panel's density, which macOS uses for its default scale.
        descriptor.sizeInMillimeters = CGSize(width: Double(width) * 0.27, height: Double(height) * 0.27)
        descriptor.vendorID = vendor
        descriptor.productID = product
        descriptor.serialNum = 1
        descriptor.terminationHandler = { _, _ in exit(0) }
        let settings = CGVirtualDisplaySettings()
        settings.hiDPI = 1
        settings.modes = [CGVirtualDisplayMode(width: width, height: height, refreshRate: 60)]
        guard let display = CGVirtualDisplay(descriptor: descriptor), display.displayID != 0, display.apply(settings) else {
            fail("cannot add a virtual display")
        }
        print(display.displayID)
        fflush(stdout)
        held = display
        // Closing stdin ends the display. It is released before the process
        // exits: a process that just exits leaves WindowServer to notice in
        // its own time, minutes later, with the display still online.
        Thread {
            while readLine() != nil {}
            DispatchQueue.main.async {
                held = nil
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { exit(0) }
            }
        }.start()
        dispatchMain()
    }
}
