class Frn < Formula
  desc "Fast Flutter run launcher with device picker"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/frn-0.2.0.tar.gz"
  sha256 "43b5a2603d71017f25997a6fabde14ece393074ffab73f429f0dea18c59637fc"
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
