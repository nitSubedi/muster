#!/bin/sh
# Renders the Homebrew formula for a release: render.sh v0.2.0 dist/SHA256SUMS
# Publish the output to the tap repo (github.com/nitSubedi/homebrew-tap) as Formula/muster.rb.
set -eu
tag="$1"; sums="$2"; repo="${REPO:-nitSubedi/muster}"
sum() { grep " $1\$" "$sums" | cut -d' ' -f1; }
cat <<RB
class Muster < Formula
  desc "Every machine you own, one AI endpoint (OpenAI/Ollama-compatible router)"
  homepage "https://github.com/$repo"
  version "${tag#v}"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/$repo/releases/download/$tag/muster-darwin-arm64"
      sha256 "$(sum muster-darwin-arm64)"
    end
    on_intel do
      url "https://github.com/$repo/releases/download/$tag/muster-darwin-amd64"
      sha256 "$(sum muster-darwin-amd64)"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/$repo/releases/download/$tag/muster-linux-arm64"
      sha256 "$(sum muster-linux-arm64)"
    end
    on_intel do
      url "https://github.com/$repo/releases/download/$tag/muster-linux-amd64"
      sha256 "$(sum muster-linux-amd64)"
    end
  end

  def install
    bin.install Dir["muster-*"].first => "muster"
  end

  def caveats
    <<~EOS
      Start Muster at login with:
        muster service install
      (Not brew services: muster manages its own per-user service.)
    EOS
  end

  test do
    assert_match "muster", shell_output("#{bin}/muster version")
  end
end
RB
