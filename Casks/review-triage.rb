cask "review-triage" do
  version "0.3.1"
  sha256 "05d3290774f9eb335d8912e909f9a6008ec692d294eb1ddaf5f529ec05e5af41"

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
