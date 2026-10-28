#!/usr/bin/env bash
# Turn bin/porthole-desktop.app into bin/Porthole-macos.dmg, signed and notarized when the
# Developer ID secrets are present (see .github/workflows/release.yml), else ad-hoc signed.
set -euo pipefail
cd "$(dirname "$0")/../.."

app=bin/Porthole.app
dmg="bin/Porthole-macos.dmg"
rm -rf "$app" "$dmg"
mv bin/porthole-desktop.app "$app"

identity=""
if [ -n "${MACOS_CERT_P12_BASE64:-}" ]; then
  kc="$RUNNER_TEMP/porthole-signing.keychain-db"
  kcpw=$(openssl rand -hex 16)
  security create-keychain -p "$kcpw" "$kc"
  security set-keychain-settings -lut 3600 "$kc"
  security unlock-keychain -p "$kcpw" "$kc"
  echo "$MACOS_CERT_P12_BASE64" | base64 --decode > "$RUNNER_TEMP/cert.p12"
  security import "$RUNNER_TEMP/cert.p12" -k "$kc" -P "${MACOS_CERT_PASSWORD:-}" -T /usr/bin/codesign
  rm -f "$RUNNER_TEMP/cert.p12"
  security set-key-partition-list -S apple-tool:,apple: -s -k "$kcpw" "$kc" >/dev/null
  security list-keychains -d user -s "$kc" $(security list-keychains -d user | tr -d '"')
  identity=$(security find-identity -v -p codesigning "$kc" | grep -o '"Developer ID Application: [^"]*"' | head -1 | tr -d '"')
  [ -n "$identity" ] || { echo "::error::the certificate has no 'Developer ID Application' identity"; exit 1; }
  codesign --force --deep --options runtime --timestamp --sign "$identity" "$app"
  codesign --verify --deep --strict --verbose=2 "$app"
else
  echo "::warning::MACOS_CERT_P12_BASE64 is not set: the app is ad-hoc signed and macOS will block the first open"
fi

stage=$(mktemp -d)
cp -R "$app" "$stage/"
ln -s /Applications "$stage/Applications"
for i in 1 2 3; do  # hdiutil on CI runners sometimes fails with "Resource busy"
  hdiutil create -volname Porthole -srcfolder "$stage" -ov -format UDZO "$dmg" && break
  sleep 5
done
[ -f "$dmg" ]

if [ -n "$identity" ]; then
  codesign --force --timestamp --sign "$identity" "$dmg"
  # Notarize with an App Store Connect API key (preferred) or an Apple ID + app-specific password.
  auth=()
  if [ -n "${APPLE_API_KEY_P8_BASE64:-}" ]; then
    echo "$APPLE_API_KEY_P8_BASE64" | base64 --decode > "$RUNNER_TEMP/notary.p8"
    auth=(--key "$RUNNER_TEMP/notary.p8" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER")
  elif [ -n "${APPLE_ID:-}" ]; then
    auth=(--apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD")
  fi
  if [ ${#auth[@]} -gt 0 ]; then
    out=$(xcrun notarytool submit "$dmg" "${auth[@]}" --wait --output-format json)
    echo "$out"
    if ! echo "$out" | grep -q '"status" *: *"Accepted"'; then
      id=$(echo "$out" | sed -n 's/.*"id" *: *"\([^"]*\)".*/\1/p')
      [ -n "$id" ] && xcrun notarytool log "$id" "${auth[@]}" || true
      echo "::error::notarization was not accepted"; exit 1
    fi
    xcrun stapler staple "$dmg"
    rm -f "$RUNNER_TEMP/notary.p8"
  else
    echo "::warning::no notarization credentials (APPLE_API_KEY_* or APPLE_ID): signed but not notarized"
  fi
fi
echo "built $dmg"
