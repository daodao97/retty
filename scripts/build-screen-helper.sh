#!/bin/sh
# Builds native/retty-screen into resources/darwin/retty-screen, a universal
# binary that `mygo build` ships in Retty.app for every Mac architecture.
# Run it before a desktop build; without it the app offers no screen sharing.
set -eu
cd "$(dirname "$0")/.."
src=native/retty-screen/main.swift
out=resources/darwin/retty-screen
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for arch in arm64 x86_64; do
	xcrun swiftc -O -swift-version 5 -target "$arch-apple-macos13.0" \
		-import-objc-header native/retty-screen/CGVirtualDisplay.h -o "$tmp/$arch" "$src"
done
mkdir -p resources/darwin
lipo -create -output "$out" "$tmp/arm64" "$tmp/x86_64"
echo "built $out"
