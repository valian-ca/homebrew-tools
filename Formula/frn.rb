class Frn < Formula
  desc "Fast Flutter run launcher with device picker"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/frn-0.2.1.tar.gz"
  sha256 "40d01a7c341d8339c5c7987606736bbb56ac371146f1b433f3f6c8e386c3dd60"
  license "MIT"

  depends_on "gum"
  depends_on "jq"

  def install
    bin.install "bin/frn"
  end

  test do
    assert_match "frn", shell_output("#{bin}/frn --help")
  end
end
