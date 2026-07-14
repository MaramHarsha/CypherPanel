"use client";

import React, { useState } from "react";
import { Copy, Check, Terminal } from "lucide-react";
import { motion } from "framer-motion";

export default function Installer() {
  const [copied, setCopied] = useState(false);
  const installCmd = "curl -fsSL https://cypherpanel.org/install.sh | bash";

  const copyToClipboard = () => {
    navigator.clipboard.writeText(installCmd);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const easeTransition = [0.16, 1, 0.3, 1];

  return (
    <section id="install" className="install-section" style={{ overflow: "hidden" }}>
      <div className="container">
        
        {/* Animated Installer Box */}
        <motion.div 
          initial={{ opacity: 0, scale: 0.95, y: 35 }}
          whileInView={{ opacity: 1, scale: 1, y: 0 }}
          viewport={{ once: true, margin: "-100px" }}
          transition={{ duration: 0.9, ease: easeTransition }}
          className="installer-box"
        >
          <h2 className="installer-title text-gradient">Deploy CypherPanel Today</h2>
          <p className="installer-subtitle">
            Bootstraps Rocky Linux, AlmaLinux, Debian, or Ubuntu servers in one command. Automatically provisions web, database, mail, and DNS servers.
          </p>

          <div className="installer-cmd-box">
            <div style={{ display: "flex", alignItems: "center", gap: "0.75rem", overflow: "hidden" }}>
              <Terminal size={18} color="var(--color-emerald)" style={{ flexShrink: 0 }} />
              <div className="installer-cmd">{installCmd}</div>
            </div>
            
            <button className="installer-copy-btn" onClick={copyToClipboard}>
              {copied ? (
                <>
                  <Check size={14} color="#10b981" />
                  <span>Copied!</span>
                </>
              ) : (
                <>
                  <Copy size={14} />
                  <span>Copy</span>
                </>
              )}
            </button>
          </div>

          <p className="installer-note">
            Requires a clean Linux environment (no pre-installed web servers) and root permissions. <br />
            Recommended minimal specifications: 1 CPU Core, 1 GB RAM, 10 GB Disk.
          </p>
        </motion.div>

      </div>
    </section>
  );
}
