class Frn < Formula
  desc "Fast Flutter run launcher with device picker"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/frn-0.3.1.tar.gz"
  sha256 "323afcd5f4e46911ed2c4bc9e5094442f1e152cf443b078a2db4e887fa335580"
  license "MIT"

  depends_on "go" => :build

  def install
    cd "cmd/frn" do
      system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}")
    end
  end

  test do
    assert_match "frn", shell_output("#{bin}/frn --help")
    assert_match version.to_s, shell_output("#{bin}/frn --version")
  end
end
