class Atelierd < Formula
  desc "Atelier dashboard daemon - local bridge to the cloud event stream"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/atelierd-0.1.5.tar.gz"
  sha256 "e6273c4ce79b64507c9de0078b84c7fd4671de188aa8aa13b57ce90026cb4f43"
  license "MIT"

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
    # ulid sub-command must produce a 26-char Crockford-base32 string.
    output = shell_output("#{bin}/atelierd ulid").strip
    assert_match(/^[0-9A-HJKMNP-TV-Z]{26}$/, output)
  end
end
