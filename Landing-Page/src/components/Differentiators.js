"use client";

import React from "react";
import { MessageSquare, GitBranch, Shield, Zap, GitPullRequest, ArrowRight } from "lucide-react";
import { motion } from "framer-motion";

export default function Differentiators() {
  const handleMouseMove = (e) => {
    const card = e.currentTarget;
    const rect = card.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    card.style.setProperty("--mouse-x", `${x}px`);
    card.style.setProperty("--mouse-y", `${y}px`);
  };

  const easeTransition = [0.16, 1, 0.3, 1];

  return (
    <section id="differentiators" className="diff-section" style={{ overflow: "hidden" }}>
      <div className="container">
        
        {/* Title Header */}
        <motion.div 
          initial={{ opacity: 0, y: 30 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true, margin: "-100px" }}
          transition={{ duration: 0.8, ease: easeTransition }}
          className="section-title-wrapper"
        >
          <span className="badge badge-amber">Exclusive Innovation</span>
          <h2 className="section-title">Features No Other Panel Ships</h2>
          <p className="section-subtitle">
            While legacy panels play catch-up, CypherPanel brings modern cloud-native workflows to standard VPS hosting.
          </p>
        </motion.div>

        {/* Bento Grid Layout */}
        <div className="diff-grid" style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: "1.5rem" }}>
          
          {/* Card 1: AI-Agent-Native (MCP Server) - Takes 2 columns span on large screens */}
          <motion.div 
            initial={{ opacity: 0, y: 40 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: "-100px" }}
            transition={{ duration: 0.8, delay: 0.1, ease: easeTransition }}
            className="mouse-glow-card mcp" 
            onMouseMove={handleMouseMove}
            style={{ gridColumn: "span 2" }}
          >
            <div className="mouse-glow-card-content diff-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", height: "100%", minHeight: "360px" }}>
              <div>
                <div className="diff-header" style={{ marginBottom: "1rem" }}>
                  <div className="diff-icon">
                    <MessageSquare size={20} />
                  </div>
                  <span className="badge badge-indigo">AI Integration</span>
                </div>
                <h3 className="diff-title" style={{ fontSize: "1.5rem", marginBottom: "0.5rem" }}>AI-Agent-Native (MCP Server)</h3>
                <p style={{ maxWidth: "580px", fontSize: "0.95rem" }}>
                  Expose the entire CypherCore API as a Model Context Protocol (MCP) server. Spin up subdomains, re-route networks, or configure SSL certificates directly from Claude or ChatGPT. Includes a diagnostic copilot parsing server logs in plain English.
                </p>
              </div>

              {/* Visual Demo block */}
              <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", border: "1px solid rgba(255,255,255,0.05)", borderRadius: "8px", padding: "1rem", marginTop: "1.5rem", fontFamily: "var(--font-mono)", fontSize: "0.8rem" }}>
                <div style={{ display: "flex", gap: "0.5rem", marginBottom: "0.5rem" }}>
                  <span style={{ color: "var(--color-indigo)" }}>[LLM Client]:</span>
                  <span style={{ color: "#fff" }}>"Generate a staging domain for app.dev and run Let's Encrypt"</span>
                </div>
                <div style={{ display: "flex", gap: "0.5rem", color: "var(--text-muted)", borderLeft: "2px solid var(--color-indigo)", paddingLeft: "0.75rem" }}>
                  <div>
                    <span style={{ display: "block", color: "var(--color-emerald)" }}>✓ Zone record added: staging.app.dev &rarr; 198.51.100.42</span>
                    <span style={{ display: "block", color: "var(--color-violet)" }}>✓ mTLS task sent: Lego solving DNS-01 challenge...</span>
                    <span style={{ display: "block", color: "#fff" }}>✓ SSL Active. Nginx vhost reloaded. URL: https://staging.app.dev</span>
                  </div>
                </div>
              </div>
            </div>
          </motion.div>

          {/* Card 2: DMARC Ingestion - Takes 1 column */}
          <motion.div 
            initial={{ opacity: 0, y: 40 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: "-100px" }}
            transition={{ duration: 0.8, delay: 0.2, ease: easeTransition }}
            className="mouse-glow-card dmarc" 
            onMouseMove={handleMouseMove}
          >
            <div className="mouse-glow-card-content diff-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", height: "100%", minHeight: "360px" }}>
              <div>
                <div className="diff-header" style={{ marginBottom: "1rem" }}>
                  <div className="diff-icon">
                    <Shield size={20} />
                  </div>
                  <span className="badge badge-amber">Deliverability</span>
                </div>
                <h3 className="diff-title" style={{ fontSize: "1.35rem", marginBottom: "0.5rem" }}>DMARC Report Ingestion</h3>
                <p style={{ fontSize: "0.9rem" }}>
                  Natively ingests aggregate DMARC (RUA) XML reports, monitors IP blacklist listings, and flags spoofing attempts directly in the user dashboard.
                </p>
              </div>

              {/* Visual Demo block */}
              <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", border: "1px solid rgba(255,255,255,0.05)", borderRadius: "8px", padding: "1rem", marginTop: "1.5rem" }}>
                <div style={{ display: "flex", justifyContent: "space-between", fontSize: "0.75rem", marginBottom: "0.4rem" }}>
                  <span style={{ color: "var(--text-muted)" }}>Mail Deliverability Rating</span>
                  <span style={{ color: "var(--color-emerald)", fontWeight: "600" }}>99.8%</span>
                </div>
                <div className="progress-bar-bg" style={{ height: "8px" }}>
                  <div className="progress-bar emerald" style={{ width: "99.8%" }}></div>
                </div>
                <div style={{ display: "flex", gap: "0.5rem", marginTop: "0.6rem", fontSize: "0.7rem", color: "var(--text-muted)" }}>
                  <span style={{ color: "var(--color-emerald)" }}>● SPF</span>
                  <span style={{ color: "var(--color-emerald)" }}>● DKIM</span>
                  <span style={{ color: "var(--color-emerald)" }}>● DMARC</span>
                </div>
              </div>
            </div>
          </motion.div>

          {/* Card 3: Declarative GitOps - Takes 1 column */}
          <motion.div 
            initial={{ opacity: 0, y: 40 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: "-100px" }}
            transition={{ duration: 0.8, delay: 0.3, ease: easeTransition }}
            className="mouse-glow-card gitops" 
            onMouseMove={handleMouseMove}
          >
            <div className="mouse-glow-card-content diff-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", height: "100%", minHeight: "360px" }}>
              <div>
                <div className="diff-header" style={{ marginBottom: "1rem" }}>
                  <div className="diff-icon">
                    <GitBranch size={20} />
                  </div>
                  <span className="badge badge-emerald">IaC</span>
                </div>
                <h3 className="diff-title" style={{ fontSize: "1.35rem", marginBottom: "0.5rem" }}>Declarative GitOps</h3>
                <p style={{ fontSize: "0.9rem" }}>
                  Define websites, zones, and cron tasks in versioned YAML configuration files. CypherCore continuously reconciles states.
                </p>
              </div>

              {/* Visual Demo block */}
              <div className="glass-panel" style={{ background: "rgba(0,0,0,0.5)", border: "1px solid rgba(255,255,255,0.05)", borderRadius: "8px", padding: "0.8rem", marginTop: "1.5rem", fontFamily: "var(--font-mono)", fontSize: "0.725rem", color: "#a5b4fc", overflowX: "auto", whiteSpace: "nowrap" }}>
                <div><span style={{ color: "#f43f5e" }}>apiVersion:</span> v1</div>
                <div><span style={{ color: "#f43f5e" }}>domain:</span> example.com</div>
                <div><span style={{ color: "#f43f5e" }}>vhost:</span></div>
                <div>&nbsp;&nbsp;<span style={{ color: "#34d399" }}>engine:</span> nginx</div>
                <div>&nbsp;&nbsp;<span style={{ color: "#34d399" }}>php:</span> 8.3</div>
                <div>&nbsp;&nbsp;<span style={{ color: "#34d399" }}>ssl:</span> auto-lego</div>
              </div>
            </div>
          </motion.div>

          {/* Card 4: Zero-Downtime Live Migration - Takes 2 columns */}
          <motion.div 
            initial={{ opacity: 0, y: 40 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: "-100px" }}
            transition={{ duration: 0.8, delay: 0.4, ease: easeTransition }}
            className="mouse-glow-card migration" 
            onMouseMove={handleMouseMove}
            style={{ gridColumn: "span 2" }}
          >
            <div className="mouse-glow-card-content diff-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", height: "100%", minHeight: "360px" }}>
              <div>
                <div className="diff-header" style={{ marginBottom: "1rem" }}>
                  <div className="diff-icon">
                    <Zap size={20} />
                  </div>
                  <span className="badge badge-rose">Zero Downtime</span>
                </div>
                <h3 className="diff-title" style={{ fontSize: "1.5rem", marginBottom: "0.5rem" }}>Zero-Downtime Account Migrations</h3>
                <p style={{ maxWidth: "580px", fontSize: "0.95rem" }}>
                  Move accounts between server nodes seamlessly. CypherAgent syncs files and databases incrementally, then routes incoming traffic from the source node to the destination node via temporary reverse proxies while DNS TTL propagates.
                </p>
              </div>

              {/* Visual Demo block */}
              <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", border: "1px solid rgba(255,255,255,0.05)", borderRadius: "8px", padding: "1rem", marginTop: "1.5rem", display: "flex", alignItems: "center", justifyContent: "space-between", gap: "1rem" }}>
                <div style={{ textAlign: "center", flex: 1 }}>
                  <span style={{ display: "block", fontSize: "0.8rem", color: "#fff", fontWeight: "600" }}>Source Node</span>
                  <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>node-us-east-01</span>
                </div>
                <div style={{ display: "flex", flexDirection: "column", alignItems: "center", flex: 1 }}>
                  <span style={{ fontSize: "0.65rem", color: "var(--color-indigo)", fontWeight: "600", marginBottom: "2px" }}>Nginx Proxy Routing</span>
                  <div style={{ display: "flex", alignItems: "center", width: "100%" }}>
                    <div style={{ height: "2px", background: "linear-gradient(90deg, var(--color-indigo), var(--color-emerald))", flex: 1 }}></div>
                    <ArrowRight size={14} color="var(--color-emerald)" style={{ marginLeft: "-4px" }} />
                  </div>
                </div>
                <div style={{ textAlign: "center", flex: 1 }}>
                  <span style={{ display: "block", fontSize: "0.8rem", color: "var(--color-emerald)", fontWeight: "600" }}>Destination Node</span>
                  <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>node-us-east-02</span>
                </div>
              </div>
            </div>
          </motion.div>

          {/* Card 5: Atomic Git Deployments - Takes 3 columns (full width) */}
          <motion.div 
            initial={{ opacity: 0, y: 40 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: "-100px" }}
            transition={{ duration: 0.8, delay: 0.5, ease: easeTransition }}
            className="mouse-glow-card gitops" 
            onMouseMove={handleMouseMove} 
            style={{ gridColumn: "1 / -1" }}
          >
            <div className="mouse-glow-card-content diff-card" style={{ display: "flex", flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: "3rem", height: "100%", minHeight: "220px" }}>
              <div style={{ flex: 1.2 }}>
                <div className="diff-header" style={{ marginBottom: "1rem" }}>
                  <div className="diff-icon">
                    <GitPullRequest size={20} />
                  </div>
                  <span className="badge badge-emerald">DevOps Pipeline</span>
                </div>
                <h3 className="diff-title" style={{ fontSize: "1.5rem", marginBottom: "0.5rem" }}>Atomic Git Deployments with Instant Rollbacks</h3>
                <p style={{ fontSize: "0.95rem" }}>
                  Every Git push deploys into a timestamped folder. Once compiled, CypherAgent flips a symbolic link to point traffic instantly. Introduce bugs? Swap the symlink back to a previous folder instantly for zero-downtime rollbacks.
                </p>
              </div>

              {/* Visual Demo block */}
              <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", border: "1px solid rgba(255,255,255,0.05)", borderRadius: "8px", padding: "1rem", flex: 1, minWidth: "300px" }}>
                <div style={{ fontSize: "0.8rem", fontWeight: "600", marginBottom: "0.8rem", color: "#fff" }}>Deployment History</div>
                
                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", fontSize: "0.75rem", padding: "0.4rem 0", borderBottom: "1px solid rgba(255,255,255,0.05)" }}>
                  <span style={{ color: "var(--color-emerald)", fontWeight: "600" }}>● release_v1.4.0 (active)</span>
                  <span style={{ color: "var(--text-muted)" }}>Active Symlink</span>
                </div>

                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", fontSize: "0.75rem", padding: "0.4rem 0", borderBottom: "1px solid rgba(255,255,255,0.05)" }}>
                  <span style={{ color: "var(--text-secondary)" }}>● release_v1.3.9</span>
                  <button className="badge badge-indigo" style={{ padding: "0.15rem 0.5rem", textTransform: "none", cursor: "pointer", fontSize: "0.65rem", border: "none" }}>Rollback</button>
                </div>

                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", fontSize: "0.75rem", padding: "0.4rem 0" }}>
                  <span style={{ color: "var(--text-secondary)" }}>● release_v1.3.8</span>
                  <span style={{ color: "var(--text-muted)" }}>4 days ago</span>
                </div>
              </div>
            </div>
          </motion.div>

        </div>

      </div>
    </section>
  );
}
