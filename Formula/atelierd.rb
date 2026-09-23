class Atelierd < Formula
  desc "Atelier dashboard daemon - local bridge to the cloud event stream"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/atelierd-0.17.1.tar.gz"
  sha256 "b83a78e33549f5b94db433ffc04512730bc904023a68ccb549aaaf9e2e749a12"
  license "MIT"
  head "https://github.com/valian-ca/homebrew-tools.git", branch: "main"

  depends_on "go" => :build

  def install
    cd "cmd/atelierd" do
      system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}")
    end
  end

  service do
    run [opt_bin/"atelierd", "run"]
    keep_alive true
    log_path "#{Dir.home}/.atelier/atelierd.stdout.log"
    error_log_path "#{Dir.home}/.atelier/atelierd.stderr.log"
  end

  test do
    assert_match "atelierd", shell_output("#{bin}/atelierd --help")
    assert_match version.to_s, shell_output("#{bin}/atelierd --version")
    assert_equal "2", shell_output("#{bin}/atelierd forge contract").strip
    output = shell_output("#{bin}/atelierd ulid").strip
    assert_match(/^[0-9A-HJKMNP-TV-Z]{26}$/, output)
  end
end
