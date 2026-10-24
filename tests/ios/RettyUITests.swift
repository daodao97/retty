import XCTest

final class RettyUITests: XCTestCase {
    func testNativeAgentApproval() throws {
        continueAfterFailure = false
        let app = XCUIApplication(bundleIdentifier: "com.daodao.retty")
        app.launchEnvironment["RETTY_UI_TEST"] = "1"
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["扫码连接桌面"].waitForExistence(timeout: 15))
        if #available(iOS 16.4, *) { app.open(URL(string: Fixture.link)!) }
        else { throw XCTSkip("URL opening requires iOS 16.4") }
        let session = app.buttons["打开会话 " + Fixture.existing]
        XCTAssertTrue(session.waitForExistence(timeout: 45), app.debugDescription)
        session.tap()
        let cancel = app.buttons["返回终端"]
        XCTAssertTrue(cancel.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(app.sheets.firstMatch.exists, "Approval must use a native action sheet")
        screenshot("native-agent-approval-codex")
        cancel.tap()
        let reopen = app.buttons["处理授权"]
        XCTAssertTrue(reopen.waitForExistence(timeout: 5))
        reopen.tap()
        let deny = app.buttons["拒绝"]
        XCTAssertTrue(deny.waitForExistence(timeout: 5))
        deny.tap()
        let allowSession = app.buttons["允许并应用建议 1（仅本会话）"]
        XCTAssertTrue(allowSession.waitForExistence(timeout: 15), app.debugDescription)
        screenshot("native-agent-approval-claude")
        allowSession.tap()
        let terminal = app.descendants(matching: .any).matching(identifier: "Terminal").firstMatch
        let completed = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value CONTAINS %@", "APPROVAL_FIXTURE_PASSED"), object: terminal)
        XCTAssertEqual(XCTWaiter.wait(for: [completed], timeout: 15), .completed, app.debugDescription)
        screenshot("native-agent-approval-completed")
    }

    func testBackgroundConnectionRetention() throws {
        continueAfterFailure = false
        let app = XCUIApplication(bundleIdentifier: "com.daodao.retty")
        app.launchEnvironment["RETTY_UI_TEST"] = "1"
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["扫码连接桌面"].waitForExistence(timeout: 15))
        if #available(iOS 16.4, *) { app.open(URL(string: Fixture.link)!) }
        else { throw XCTSkip("URL opening requires iOS 16.4") }
        let existing = app.buttons["打开会话 " + Fixture.existing]
        XCTAssertTrue(existing.waitForExistence(timeout: 45), app.debugDescription)
        existing.tap()
        let terminal = app.descendants(matching: .any).matching(identifier: "Terminal").firstMatch
        XCTAssertTrue(terminal.waitForExistence(timeout: 10))
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<8 where !app.keys["q"].exists { app.buttons["Next keyboard"].tap() }
        XCTAssertTrue(app.keys["q"].exists)
        app.typeText("touch '" + Fixture.directory + "/background-start'\n")
        app.buttons["收起"].tap()
        screenshot("background-before")
        XCUIDevice.shared.press(.home)
        Thread.sleep(forTimeInterval: 45)
        let started = Date()
        app.activate()
        XCTAssertTrue(terminal.waitForExistence(timeout: 8), app.debugDescription)
        let resumeReady = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: app.buttons["连接恢复操作"])
        XCTAssertEqual(XCTWaiter.wait(for: [resumeReady], timeout: 8), .completed)
        XCTAssertTrue(terminal.isHittable)
        screenshot("background-resumed")
        print("BACKGROUND_RESUME_SECONDS", Date().timeIntervalSince(started))
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        app.typeText("touch '" + Fixture.directory + "/background-returned'\n")
        app.typeText("touch '" + Fixture.directory + "/disconnect-control'\n")
        // Recovery temporarily removes input controls; do not try to tap an
        // accessory key while the fixture is deliberately closing control.
        Thread.sleep(forTimeInterval: 4)
        XCTAssertTrue(terminal.waitForExistence(timeout: 8), app.debugDescription)
        let controlReady = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: app.buttons["连接恢复操作"])
        XCTAssertEqual(XCTWaiter.wait(for: [controlReady], timeout: 8), .completed)
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        app.typeText("touch '" + Fixture.directory + "/redial-returned'\n")
        screenshot("background-control-recovered")
    }

    func nineKey(_ app: XCUIApplication, _ letters: String) -> XCUIElement {
        // The native nine-key labels include spaces between and after letters.
        let pattern = "\\s*" + letters.map(String.init).joined(separator: "\\s*") + "\\s*"
        return app.keys.matching(NSPredicate(format: "label MATCHES[c] %@", pattern)).firstMatch
    }

    func testInputContinuation() throws {
        continueAfterFailure = false
        let app = XCUIApplication(bundleIdentifier: "com.daodao.retty")
        app.launchEnvironment["RETTY_UI_TEST"] = "1"
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["扫码连接桌面"].waitForExistence(timeout: 15))
        if #available(iOS 16.4, *) { app.open(URL(string: Fixture.link)!) }
        else { throw XCTSkip("URL opening requires iOS 16.4") }
        let existing = app.buttons["打开会话 " + Fixture.existing]
        XCTAssertTrue(existing.waitForExistence(timeout: 45), app.debugDescription)
        existing.tap()
        let terminal = app.descendants(matching: .any).matching(identifier: "Terminal").firstMatch
        XCTAssertTrue(terminal.waitForExistence(timeout: 10))
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10))
        func english() {
            for _ in 0..<8 where !app.keys["q"].exists { app.buttons["Next keyboard"].tap() }
            XCTAssertTrue(app.keys["q"].exists)
        }
        func chinese() {
            for _ in 0..<8 where !nineKey(app, "MNO").exists { app.buttons["Next keyboard"].tap() }
            XCTAssertTrue(nineKey(app, "MNO").exists)
            for key in ["MNO", "GHI", "GHI", "ABC", "MNO"] { nineKey(app, key).tap() }
            let candidate = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "你好")).firstMatch
            XCTAssertTrue(candidate.waitForExistence(timeout: 5))
            candidate.tap()
        }
        english()
        app.typeText("printf '%s' '")
        for letter in ["a", "b", "c"] { app.keys[letter].tap() }
        chinese()
        english()
        app.typeText("XYZ")
        chinese()
        english()
        app.typeText("tail' > '" + Fixture.directory + "/input-result'\n")
        screenshot("input-english-chinese-continuation")
        // The routing fix must also retain the earlier scroll-dismiss fix.
        let gone = NSPredicate(format: "exists == false")
        for upward in [true, false] {
            terminal.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
            if upward { terminal.swipeUp() } else { terminal.swipeDown() }
            let hidden = XCTNSPredicateExpectation(predicate: gone, object: app.keyboards.firstMatch)
            XCTAssertEqual(XCTWaiter.wait(for: [hidden], timeout: 5), .completed)
        }
    }
    func screenshot(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    func edgeBack(_ app: XCUIApplication, y: CGFloat, complete: Bool = true) {
        let origin = app.coordinate(withNormalizedOffset: .zero)
        let start = origin.withOffset(CGVector(dx: 5, dy: y))
        let end = origin.withOffset(CGVector(dx: app.frame.width * (complete ? 0.85 : 0.15), dy: y))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.3)
    }

    func testRemoteSessionFlow() throws {
        continueAfterFailure = false
        let app = XCUIApplication(bundleIdentifier: "com.daodao.retty")
        app.launchEnvironment["RETTY_UI_TEST"] = "1"
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["扫码连接桌面"].waitForExistence(timeout: 15))
        let overlay = app.otherElements["MyGo launch screen"]
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: overlay)
        waitForExpectations(timeout: 10)
        screenshot("connect")

        addUIInterruptionMonitor(withDescription: "System permissions") { alert in
            for label in ["Allow", "允许", "无线局域网与蜂窝网络", "WLAN & Cellular"] {
                if alert.buttons[label].exists { alert.buttons[label].tap(); return true }
            }
            let ok = alert.buttons["OK"]
            if ok.exists { ok.tap(); return true }
            return false
        }
        app.buttons["扫码连接桌面"].tap()
        // Trigger the permission interruption monitor when iOS displays it.
        if !app.buttons["取消"].waitForExistence(timeout: 3) &&
            XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch.exists { app.tap() }
        // A phone pointing at a real desktop QR can finish scanning before
        // XCTest finds Cancel. Exercise cancellation only while it is shown.
        if app.buttons["取消"].waitForExistence(timeout: 2) {
            screenshot("native-scanner")
            app.buttons["取消"].tap()
            XCTAssertTrue(app.buttons["扫码连接桌面"].waitForExistence(timeout: 10))
        }

        if #available(iOS 16.4, *) { app.open(URL(string: Fixture.link)!) }
        else { throw XCTSkip("URL opening requires iOS 16.4") }
        let permission = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch
        if permission.waitForExistence(timeout: 2) {
            for label in ["Allow", "允许", "无线局域网与蜂窝网络", "WLAN & Cellular"] {
                if permission.buttons[label].exists { permission.buttons[label].tap(); break }
            }
        }
        let existing = app.buttons["打开会话 " + Fixture.existing]
        XCTAssertTrue(existing.waitForExistence(timeout: 45), app.debugDescription)
        screenshot("sessions")
        existing.press(forDuration: 0.8)
        let name = app.descendants(matching: .any).matching(identifier: "会话名称").firstMatch
        XCTAssertTrue(name.waitForExistence(timeout: 5), "Session long press did not show settings")
        screenshot("session-settings")
        name.tap()
        for _ in 0..<8 where !app.keys["q"].exists { app.buttons["Next keyboard"].tap() }
        app.typeText("Phone fixture")
        app.buttons["置顶会话"].tap()
        app.buttons["保存"].tap()
        let renamed = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value CONTAINS %@ AND value CONTAINS %@", "Phone fixture", "置顶"), object: existing)
        XCTAssertEqual(XCTWaiter.wait(for: [renamed], timeout: 5), .completed)
        screenshot("session-pinned-renamed")
        existing.tap()
        screenshot("after-open-existing")
        let terminal = app.descendants(matching: .any).matching(identifier: "Terminal").firstMatch
        XCTAssertTrue(terminal.waitForExistence(timeout: 15), app.debugDescription)
        terminal.swipeDown()
        XCTAssertFalse(app.keyboards.firstMatch.exists, "Scrolling opened the keyboard")
        screenshot("terminal-touch-scroll")
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10))
        // A focused terminal uses a native TextInputClient. Its input traits
        // must still dismiss an already-visible keyboard in either direction.
        for upward in [true, false] {
            if upward { terminal.swipeUp() } else { terminal.swipeDown() }
            let hidden = XCTNSPredicateExpectation(predicate: gone, object: app.keyboards.firstMatch)
            XCTAssertEqual(XCTWaiter.wait(for: [hidden], timeout: 5), .completed,
                           "Scrolling the focused terminal retained its keyboard")
            XCTAssertFalse(app.buttons["Esc"].exists, "Scroll retained the keyboard accessory")
            terminal.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        }
        for _ in 0..<8 where !app.keys["q"].exists {
            app.buttons["Next keyboard"].tap()
        }
        XCTAssertTrue(app.keys["q"].exists, "English keyboard unavailable for shell fixture")
        for label in ["Esc", "Ctrl", "Option", "Cmd", "←", "→", "更多", "收起"] {
            let key = app.buttons[label]
            XCTAssertTrue(key.isHittable, "Accessory action hidden: " + label)
            XCTAssertGreaterThanOrEqual(key.frame.width, 44)
            XCTAssertGreaterThanOrEqual(key.frame.height, 44)
        }
        app.buttons["更多"].tap()
        XCTAssertTrue(app.buttons["粘贴"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["粘贴"].isHittable)
        XCTAssertEqual(app.otherElements["Input accessory"].frame.height, 88, accuracy: 1)
        for label in ["Shift", "Tab", "←", "→", "↑", "↓", "换行", "粘贴"] {
            XCTAssertTrue(app.buttons[label].isHittable, "Expanded key hidden: " + label)
            XCTAssertGreaterThanOrEqual(app.buttons[label].frame.width, 44)
            XCTAssertGreaterThanOrEqual(app.buttons[label].frame.height, 44)
        }
        XCTAssertTrue(app.keyboards.firstMatch.exists, "Opening secondary keys dismissed the keyboard")
        screenshot("keyboard-accessory-expanded")
        app.buttons["更多"].tap()
        app.buttons["收起"].tap()
        let keyboardGone = XCTNSPredicateExpectation(predicate: gone, object: app.keyboards.firstMatch)
        XCTAssertEqual(XCTWaiter.wait(for: [keyboardGone], timeout: 5), .completed)
        XCTAssertFalse(app.buttons["Esc"].exists, "Reading mode retained input accessory")
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10))
        screenshot("keyboard-accessory-compact")
        // Edit earlier characters directly in the terminal; no composer is
        // involved, and arrows remain available without opening More.
        app.typeText("printf 'RETTY_IPHONE_EDIT_abcd'")
        app.buttons["←"].tap()
        app.buttons["←"].tap()
        app.typeText(XCUIKeyboardKey.delete.rawValue)
        app.typeText("Z")
        app.buttons["→"].tap()
        app.buttons["→"].tap()
        app.typeText("\n")
        // The accessory must preserve the native nine-key candidate session.
        app.typeText("printf 'RETTY_IPHONE_IME_")
        for _ in 0..<8 where !nineKey(app, "MNO").exists {
            app.buttons["Next keyboard"].tap()
        }
        XCTAssertTrue(nineKey(app, "MNO").exists, "Chinese nine-key keyboard unavailable: " + app.debugDescription)
        for key in ["MNO", "GHI", "GHI", "ABC", "MNO"] { nineKey(app, key).tap() }
        let candidate = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "你好")).firstMatch
        XCTAssertTrue(candidate.waitForExistence(timeout: 5))
        app.buttons["更多"].tap()
        XCTAssertTrue(candidate.isHittable, "Expanding keys lost the IME candidate")
        screenshot("keyboard-nine-key-expanded")
        app.buttons["更多"].tap()
        XCTAssertTrue(candidate.isHittable, "Collapsing keys lost the IME candidate")
        candidate.tap()
        for _ in 0..<8 where !app.keys["q"].exists { app.buttons["Next keyboard"].tap() }
        XCTAssertTrue(app.keys["q"].exists)
        app.typeText("\\n'\n")
        app.typeText("printf 'RETTY_IPHONE_KEYBOARD_OX")
        app.typeText(XCUIKeyboardKey.delete.rawValue)
        app.typeText("K\\n'\n")
        screenshot("existing-terminal-keyboard")
        // Native modifier gestures switch to letters, lock across multiple
        // keys and release on the next tap. All input targets this fixture.
        for _ in 0..<8 where !nineKey(app, "MNO").exists { app.buttons["Next keyboard"].tap() }
        XCTAssertTrue(nineKey(app, "MNO").exists)
        let ctrl = app.buttons["Ctrl"]
        ctrl.press(forDuration: 0.6)
        XCTAssertEqual(ctrl.value as? String, "已锁定")
        XCTAssertTrue(app.keys["c"].waitForExistence(timeout: 5))
        screenshot("keyboard-modifier-locked")
        app.keys["c"].tap()
        XCTAssertEqual(ctrl.value as? String, "已锁定")
        app.keys["c"].tap()
        ctrl.tap()
        XCTAssertEqual(ctrl.value as? String, "未启用")
        ctrl.tap()
        XCTAssertEqual(ctrl.value as? String, "下一次输入")
        app.keys["c"].tap()
        XCTAssertEqual(ctrl.value as? String, "未启用")
        for _ in 0..<8 where !app.keys["q"].exists { app.buttons["Next keyboard"].tap() }
        XCTAssertTrue(app.keys["q"].exists)
        app.typeText("touch '" + Fixture.directory + "/disconnect-phone'\n")
        let reconnect = app.descendants(matching: .any).matching(identifier: "正在重连").firstMatch
        XCTAssertTrue(reconnect.waitForExistence(timeout: 10), "Controlled transport loss did not enter reconnect")
        XCTAssertTrue(terminal.exists, "Reconnect discarded the terminal page")
        screenshot("terminal-reconnecting")
        let restored = XCTNSPredicateExpectation(predicate: gone, object: reconnect)
        XCTAssertEqual(XCTWaiter.wait(for: [restored], timeout: 45), .completed)
        XCTAssertTrue(terminal.exists, "Reconnect did not return to the existing terminal")
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10))
        app.typeText("printf 'RETTY_IPHONE_RECONNECTED_OK\\n'\n")
        screenshot("terminal-auto-reconnected")
        XCTAssertTrue(app.keyboards.firstMatch.exists, "Ctrl+C dismissed the keyboard")
        app.typeText("printf '\\033[2J\\033[Hselection word\\n'\n")
        Thread.sleep(forTimeInterval: 2)
        let selectionStart = terminal.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 40, dy: 12))
        let selectionEnd = terminal.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 104, dy: 12))
        selectionStart.press(forDuration: 0.8)
        XCTAssertTrue(app.buttons["复制"].waitForExistence(timeout: 5), "Long press did not select text with keyboard visible")
        screenshot("terminal-long-press-keyboard")
        edgeBack(app, y: terminal.frame.midY, complete: false)
        XCTAssertTrue(terminal.exists, "Cancelled edge gesture left the terminal")
        XCTAssertTrue(app.keyboards.firstMatch.exists, "Cancelled edge gesture dismissed the keyboard")
        XCTAssertTrue(app.buttons["复制"].exists, "Cancelled edge gesture lost the selection")
        screenshot("terminal-edge-back-cancelled")
        edgeBack(app, y: terminal.frame.midY)
        XCTAssertTrue(existing.waitForExistence(timeout: 10), "Terminal edge gesture did not return to sessions")
        XCTAssertFalse(app.keyboards.firstMatch.exists)
        screenshot("terminal-edge-back")
        existing.tap()
        XCTAssertTrue(terminal.waitForExistence(timeout: 15))
        XCTAssertFalse(app.keyboards.firstMatch.exists)
        selectionStart.press(forDuration: 0.8, thenDragTo: selectionEnd)
        XCTAssertTrue(app.buttons["复制"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.keyboards.firstMatch.exists, "Long press selection opened the keyboard")
        screenshot("terminal-long-press-drag")
        app.buttons["复制"].tap()
        terminal.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10))
        app.typeText("printf '\\nRETTY_IPHONE_SELECTION_%s\\n' '")
        app.buttons["更多"].tap()
        XCTAssertTrue(app.buttons["粘贴"].isHittable)
        app.buttons["粘贴"].tap()
        XCTAssertTrue(app.keyboards.firstMatch.exists, "Pasting dismissed the keyboard")
        app.buttons["更多"].tap()
        app.typeText("'\n")
        app.buttons["返回"].tap()
        XCTAssertTrue(app.buttons["新建会话"].waitForExistence(timeout: 10))
        app.buttons["新建会话"].tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "工作目录").firstMatch.waitForExistence(timeout: 10))
        screenshot("new-session")
        edgeBack(app, y: app.frame.height * 0.5)
        XCTAssertTrue(existing.waitForExistence(timeout: 10), "New-session edge gesture did not return to sessions")
        app.buttons["新建会话"].tap()
        XCTAssertTrue(app.buttons["创建并打开"].waitForExistence(timeout: 10))
        app.buttons["创建并打开"].tap()
        XCTAssertTrue(terminal.waitForExistence(timeout: 20), app.debugDescription)
        screenshot("created-terminal")

        XCUIDevice.shared.orientation = .landscapeLeft
        screenshot("landscape-terminal")
        XCUIDevice.shared.orientation = .portrait
        XCUIDevice.shared.press(.home)
        app.activate()
        XCTAssertTrue(terminal.waitForExistence(timeout: 45), app.debugDescription)
        screenshot("resumed-terminal")
        edgeBack(app, y: terminal.frame.midY)
        XCTAssertTrue(existing.waitForExistence(timeout: 10))
        edgeBack(app, y: app.frame.height * 0.5)
        let recent = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ OR label BEGINSWITH %@", "重新连接 ", "打开桌面 ")).firstMatch
        XCTAssertTrue(recent.waitForExistence(timeout: 10), "Sessions edge gesture did not return to connection history")
        screenshot("sessions-edge-back")
        app.terminate()
        app.launch()
        XCTAssertTrue(recent.waitForExistence(timeout: 15))
        recent.tap()
        XCTAssertTrue(existing.waitForExistence(timeout: 45), app.debugDescription)
        XCTAssertTrue((existing.value as? String)?.contains("Phone fixture") == true, "Mobile name was not restored from Keychain")
        XCTAssertTrue((existing.value as? String)?.contains("置顶") == true, "Pinned setting was not persisted")
        screenshot("reconnected-sessions")
    }

    func testRemotePushRegistration() throws {
        continueAfterFailure = false
        let app = XCUIApplication(bundleIdentifier: "com.daodao.retty")
        app.launchEnvironment["RETTY_UI_TEST"] = "1"
        app.terminate()
        app.launch()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        // A newly installed app on a China-region device may ask for network
        // access as well as notification permission. Handle both launch alerts.
        for _ in 0..<2 where springboard.alerts.firstMatch.waitForExistence(timeout: 3) {
            var accepted = false
            for label in ["无线局域网与蜂窝网络", "WLAN & Cellular", "Allow", "允许", "好", "OK"] {
                if springboard.alerts.firstMatch.buttons[label].exists {
                    springboard.alerts.firstMatch.buttons[label].tap()
                    accepted = true
                    break
                }
            }
            if !accepted { break }
        }
        XCTAssertTrue(app.buttons["扫码连接桌面"].waitForExistence(timeout: 15))
        if #available(iOS 16.4, *) { app.open(URL(string: Fixture.link)!) }
        else { throw XCTSkip("URL opening requires iOS 16.4") }
        XCTAssertTrue(app.buttons["打开会话 " + Fixture.existing].waitForExistence(timeout: 45), app.debugDescription)
        XCTAssertTrue(app.switches["后台任务提醒"].waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertFalse(app.staticTexts["无法注册后台通知"].exists)
        XCTAssertFalse(app.staticTexts["无法注册后台通知，请稍后重试"].exists)
        screenshot("remote-push-registration-sessions")
    }
}
