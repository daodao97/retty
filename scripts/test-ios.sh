#!/bin/bash
# Exercise an installed iOS app against isolated desktop sessions over Tailcat.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
: "${IOS_TEAM:?Set IOS_TEAM to your Apple development team}"
: "${IOS_DEVICE:?Set IOS_DEVICE to the connected iPhone UDID}"
WORK="$ROOT/.mygo/ios-test"
mkdir -p "$WORK"
python3 - "$WORK" <<'PYCODE'
from pathlib import Path
import sys
work=Path(sys.argv[1])
for name in ('fixture.json','ios-finished'): (work/name).unlink(missing_ok=True)
PYCODE
case "${IOS_TEST_FLOW:-full}" in
 full)
  TEST_CASE=RettyUITests/RettyUITests/testRemoteSessionFlow
  RETTY_REMOTE_E2E=1 RETTY_IOS_FIXTURE="$WORK/fixture.json" \
   go test ./internal/remote -run '^TestTailcatSessionLifecycle$' -v -timeout 25m > "$WORK/host.log" 2>&1 &
  ;;
 background)
  TEST_CASE=RettyUITests/RettyUITests/testBackgroundConnectionRetention
  RETTY_IOS_BACKGROUND_FIXTURE="$WORK/fixture.json" \
   go test ./internal/remote -run '^TestTailcatIOSBackgroundRetention$' -v -timeout 6m > "$WORK/host.log" 2>&1 &
  ;;
 input)
  TEST_CASE=RettyUITests/RettyUITests/testInputContinuation
  RETTY_IOS_INPUT_FIXTURE="$WORK/fixture.json" \
   go test ./internal/remote -run '^TestTailcatIOSInputContinuation$' -v -timeout 5m > "$WORK/host.log" 2>&1 &
  ;;
 *) echo "IOS_TEST_FLOW must be full, background or input" >&2; exit 1 ;;
esac
HOST_PID=$!
finish() {
 touch "$WORK/ios-finished"
 wait "$HOST_PID" || true
 rm -f tests/ios/Fixture.swift
}
trap finish EXIT
for ((attempt=0; attempt<45; attempt++)); do
 [[ -f "$WORK/fixture.json" ]] && break
 if ! kill -0 "$HOST_PID" 2>/dev/null; then cat "$WORK/host.log"; exit 1; fi
 sleep 1
done
[[ -f "$WORK/fixture.json" ]] || { echo "Desktop fixture did not start; see $WORK/host.log"; exit 1; }
python3 - "$WORK/fixture.json" <<'PY'
import json,sys,os
fixture=json.load(open(sys.argv[1]))
path='tests/ios/Fixture.swift'
with open(path,'w') as out:
 out.write('enum Fixture {\n')
 for key in ('Link','Existing','Created','Directory'):
  out.write(' static let '+key.lower()+' = '+json.dumps(fixture[key],ensure_ascii=False)+'\n')
 out.write('}\n')
os.chmod(path,0o600)
PY
xcrun devicectl device install app --device "$IOS_DEVICE" build/ios-arm64/Retty.app
RESULT="$WORK/$(date +%Y%m%d-%H%M%S).xcresult"
xcodebuild -quiet -project tests/ios/Tests.xcodeproj -scheme RettyUITests \
 -sdk iphoneos -destination "id=$IOS_DEVICE" -derivedDataPath "$WORK/derived" \
 -only-testing:"$TEST_CASE" \
 -resultBundlePath "$RESULT" -collect-test-diagnostics never \
 -test-timeouts-enabled YES -maximum-test-execution-time-allowance 180 \
 "DEVELOPMENT_TEAM=$IOS_TEAM" -allowProvisioningUpdates -allowProvisioningDeviceRegistration test
touch "$WORK/ios-finished"
wait "$HOST_PID"
rm -f tests/ios/Fixture.swift
trap - EXIT
echo "iPhone workflow and desktop session checks passed: $RESULT"
