cask "review-triage" do
  version "0.4.0"
  sha256 "6182e65247352cf4bdec808ad6e1fd709321fbe27fa8207c89a9150579cd8a1a"

  url "https://github.com/valian-ca/homebrew-tools/releases/download/review-triage-app-#{version}/ReviewTriage-#{version}.zip"
  name "Review Triage"
  desc "Native macOS app for triaging code-review findings"
  homepage "https://github.com/valian-ca/homebrew-tools"

  depends_on macos: ">= :sequoia"

  app "ReviewTriage.app"
  binary "#{appdir}/ReviewTriage.app/Contents/MacOS/review-triage-cli", target: "review-triage"

  zap trash: [
    "~/Library/Application Support/ca.valian.review-triage",
    "~/Library/Preferences/ca.valian.review-triage.plist",
    "~/Library/Caches/ca.valian.review-triage",
  ]
end
