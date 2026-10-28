#!/bin/bash
# Build libghostty-vt for the selected SDK, then package the native Retty app.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$ROOT"
GHOSTTY_COMMIT=befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028
ZIG_VERSION=0.16.0
WORK="$ROOT/.mygo/ios"
mkdir -p "$WORK"
if [[ ! -x "$WORK/zig-aarch64-macos-$ZIG_VERSION/zig" ]]; then
 curl -fL "https://ziglang.org/download/$ZIG_VERSION/zig-aarch64-macos-$ZIG_VERSION.tar.xz" -o "$WORK/zig.tar.xz"
 tar -xf "$WORK/zig.tar.xz" -C "$WORK"
fi
if [[ ! -d "$WORK/ghostty/.git" ]]; then
 git clone --filter=blob:none --no-checkout https://github.com/ghostty-org/ghostty.git "$WORK/ghostty"
fi
git -C "$WORK/ghostty" checkout "$GHOSTTY_COMMIT"
TARGET=aarch64-ios
PREFIX="$WORK/ghostty-device"
for arg in "$@"; do
 if [[ "$arg" == -ios-simulator ]]; then TARGET=aarch64-ios-simulator; PREFIX="$WORK/ghostty-simulator"; fi
done
(cd "$WORK/ghostty" && "$WORK/zig-aarch64-macos-$ZIG_VERSION/zig" build -Demit-lib-vt -Dtarget="$TARGET" -Doptimize=ReleaseFast -Demit-xcframework=false --prefix "$PREFIX")
PLATFORM=ios
SDK=iphoneos
if [[ "$TARGET" == aarch64-ios-simulator ]]; then PLATFORM=ios-simulator; SDK=iphonesimulator; fi
SDK_VERSION=$(xcrun --sdk "$SDK" --show-sdk-version)
python3 scripts/generate-ios-vt.py
gofmt -w internal/terminal/internal/vt/open_ios.go
xcrun ld -r -arch arm64 -platform_version "$PLATFORM" 15.0 "$SDK_VERSION" -all_load "$PREFIX/lib/libghostty-vt.a" -o internal/terminal/internal/vt/ghostty_ios_arm64.syso
# MyGo currently shares one icon setting across platforms. Use an isolated
# config for the full-bleed iOS artwork without changing the desktop config.
ICON_CONFIG_DIR=$(mktemp -d "$WORK/build-config-XXXXXX")
trap 'rm -rf "$ICON_CONFIG_DIR"' EXIT
python3 - "$ROOT" "$ICON_CONFIG_DIR" <<'PY'
import json, os, sys
from pathlib import Path
root, stage = map(Path, sys.argv[1:])
config = json.loads((root / 'mygo.json').read_text())
# Desktop self-updates are unavailable on iOS; TestFlight/App Store own updates.
config.pop('updates', None)
config['main'] = str(root / config.get('main', '.'))
config['out'] = str(root / config.get('out', 'build'))
config['icon'] = str(root / 'resources/ios/app-icon.png')
for key in ('bindings', 'frontendDist'):
    if config.get(key):
        config[key] = str(root / config[key])
config['resources'] = [str(root / path) for path in config.get('resources', [])]
ios = config.setdefault('ios', {})
for target in [ios, *ios.get('extensions', [])]:
    native = target.get('native', {})
    for key in ('sources', 'resources', 'libraries'):
        if key in native:
            native[key] = [str(root / path) for path in native[key]]
    if native.get('entryPoint'):
        native['entryPoint'] = str(root / native['entryPoint'])
launch = config.get('ios', {}).get('launchScreen', {})
if launch.get('image'):
    launch['image'] = str(root / launch['image'])
if os.environ.get('IOS_PROVISIONING_PROFILE'):
    signing = config.setdefault('ios', {}).setdefault('signing', {})
    signing['style'] = 'manual'
    signing['provisioningProfile'] = os.environ['IOS_PROVISIONING_PROFILE']
    if os.environ.get('IOS_ACTIVITY_PROVISIONING_PROFILE'):
        signing.setdefault('exportProfiles', {})['com.daodao.retty.RettyActivity'] = os.environ['IOS_ACTIVITY_PROVISIONING_PROFILE']
    if os.environ.get('IOS_SIGNING_IDENTITY'):
        signing['identity'] = os.environ['IOS_SIGNING_IDENTITY']
(stage / 'mygo.json').write_text(json.dumps(config, indent=2) + '\n')
(stage / 'resources').symlink_to(root / 'resources', target_is_directory=True)
PY
go tool mygo build -platform ios/arm64 "$@" "$ICON_CONFIG_DIR"
