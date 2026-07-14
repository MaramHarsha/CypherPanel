"use client";

import React, { useState } from "react";
import { Server, Settings, Users, Cloud, Globe, FolderOpen, Database, Mail, ShieldCheck, ArrowRight } from "lucide-react";
import { motion } from "framer-motion";

export default function Features() {
  const [activeTab, setActiveTab] = useState("admin");

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
    <section id="features" className="features-section" style={{ overflow: "hidden" }}>
      <div className="container">
        
        {/* Title Section */}
        <motion.div 
          initial={{ opacity: 0, y: 30 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true, margin: "-100px" }}
          transition={{ duration: 0.8, ease: easeTransition }}
          className="section-title-wrapper"
        >
          <span className="badge badge-emerald">FEATURE SET</span>
          <h2 className="section-title">Everything cPanel & WHM Do. Modernized.</h2>
          <p className="section-subtitle">
            A comprehensive suite of administrative and user-level tools running on a high-performance system layer.
          </p>
        </motion.div>

        <div className="features-container">
          
          {/* Navigation Tab buttons */}
          <motion.div 
            initial={{ opacity: 0, scale: 0.95 }}
            whileInView={{ opacity: 1, scale: 1 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6, ease: easeTransition }}
            className="tabs-nav glass-panel"
            style={{ marginBottom: "3rem" }}
          >
            <button 
              className={`tab-trigger ${activeTab === "admin" ? "active" : ""}`}
              onClick={() => setActiveTab("admin")}
            >
              Admin Panel (WHM)
            </button>
            <button 
              className={`tab-trigger ${activeTab === "user" ? "active" : ""}`}
              onClick={() => setActiveTab("user")}
            >
              User Panel (cPanel)
            </button>
          </motion.div>

          {/* Bento-Grid Features Layout */}
          <motion.div 
            key={activeTab}
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.6, ease: easeTransition }}
            className="features-grid"
            style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: "1.5rem" }}
          >
            {activeTab === "admin" ? (
              <>
                {/* 1. Fleet Node Manager - span 2 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove} style={{ gridColumn: "span 2" }}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-indigo)" }}><Server size={20} /></div>
                        <h3>Fleet Node Manager</h3>
                      </div>
                      <p>Register, monitor, and configure server nodes running CypherAgent globally from a single controller dashboard.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", padding: "0.8rem", borderRadius: "8px", border: "1px solid rgba(255,255,255,0.05)", display: "flex", justifyContent: "space-between", fontSize: "0.75rem" }}>
                      <span style={{ color: "var(--color-emerald)" }}>● Node East-Primary (Online)</span>
                      <span style={{ color: "var(--color-emerald)" }}>● Node West-Backup (Online)</span>
                      <span style={{ color: "var(--color-rose)" }}>● Node AP-Stage (Offline)</span>
                    </div>
                  </div>
                </div>

                {/* 2. Package Templates - span 1 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-indigo)" }}><Settings size={20} /></div>
                        <h3>Package Templates</h3>
                      </div>
                      <p>Define templates with bandwidth, DB, and strict cgroups CPU/Memory slices.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", padding: "0.8rem", borderRadius: "8px", border: "1px solid rgba(255,255,255,0.05)" }}>
                      <div style={{ display: "flex", justifyContent: "space-between", fontSize: "0.7rem", marginBottom: "0.25rem" }}>
                        <span>CPU Quota</span>
                        <span>50%</span>
                      </div>
                      <div className="progress-bar-bg" style={{ height: "6px" }}><div className="progress-bar indigo" style={{ width: "50%" }}></div></div>
                    </div>
                  </div>
                </div>

                {/* 3. Account Provisioner - span 1 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-indigo)" }}><Users size={20} /></div>
                        <h3>Instant Provisioning</h3>
                      </div>
                      <p>Automatically setup system users and pools on target servers under 2 seconds.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel text-gradient" style={{ background: "rgba(0,0,0,0.5)", padding: "0.6rem 0.8rem", borderRadius: "6px", border: "1px solid rgba(255,255,255,0.05)", fontFamily: "var(--font-mono)", fontSize: "0.7rem" }}>
                      $ cypher user create dev12
                    </div>
                  </div>
                </div>

                {/* 4. Deduplicated Backups - span 2 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove} style={{ gridColumn: "span 2" }}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-indigo)" }}><Cloud size={20} /></div>
                        <h3>Deduplicated Backups</h3>
                      </div>
                      <p>Schedule Borg/restic incremental backups directly to AWS S3, Backblaze B2, or SFTP drives, skipping heavy compression cycles.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", padding: "0.8rem", borderRadius: "8px", border: "1px solid rgba(255,255,255,0.05)", display: "flex", alignItems: "center", gap: "1rem", fontSize: "0.75rem" }}>
                      <span>Incremental Tree Hash</span>
                      <div style={{ height: "1px", background: "rgba(255,255,255,0.1)", flex: 1 }}></div>
                      <span className="badge badge-emerald">restic v0.16</span>
                    </div>
                  </div>
                </div>
              </>
            ) : (
              <>
                {/* 1. Domain Router - span 2 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove} style={{ gridColumn: "span 2" }}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-emerald)" }}><Globe size={20} /></div>
                        <h3>Domain Router</h3>
                      </div>
                      <p>Manage addon domains, aliases, subdirectories, and configure immediate HTTP 301/302 redirects in Nginx.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", padding: "0.8rem", borderRadius: "8px", border: "1px solid rgba(255,255,255,0.05)", display: "flex", alignItems: "center", justifyContent: "space-between", gap: "1rem", fontSize: "0.75rem" }}>
                      <span>dev.app.com</span>
                      <ArrowRight size={14} color="var(--color-emerald)" />
                      <span>/var/www/dev_app/public</span>
                    </div>
                  </div>
                </div>

                {/* 2. DNS Zone Editor - span 1 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-emerald)" }}><Settings size={20} /></div>
                        <h3>DNS Zone Editor</h3>
                      </div>
                      <p>Complete visual layout to add A, AAAA, MX, TXT, and SRV records with instant PowerDNS syncing.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", padding: "0.6rem 0.8rem", borderRadius: "8px", border: "1px solid rgba(255,255,255,0.05)", fontFamily: "var(--font-mono)", fontSize: "0.7rem", color: "#fff" }}>
                      <div>A &rarr; 198.51.100.42</div>
                      <div>MX &rarr; mail.example.com</div>
                    </div>
                  </div>
                </div>

                {/* 3. File Explorer - span 1 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-emerald)" }}><FolderOpen size={20} /></div>
                        <h3>File Manager</h3>
                      </div>
                      <p>Responsive, drag-and-drop file explorer with inline editor, zip compilation, and Pure-FTPd virtual directories.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", padding: "0.8rem", borderRadius: "8px", border: "1px solid rgba(255,255,255,0.05)", display: "flex", gap: "0.5rem", fontSize: "0.7rem" }}>
                      <span style={{ color: "var(--color-amber)" }}>📂 public_html</span>
                      <span style={{ color: "var(--text-muted)" }}>/</span>
                      <span style={{ color: "#fff" }}>📄 index.php</span>
                    </div>
                  </div>
                </div>

                {/* 4. Database Controller - span 2 */}
                <div className="mouse-glow-card" onMouseMove={handleMouseMove} style={{ gridColumn: "span 2" }}>
                  <div className="mouse-glow-card-content feature-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", minHeight: "260px" }}>
                    <div>
                      <div className="feature-title-row">
                        <div className="feature-icon-wrapper" style={{ color: "var(--color-emerald)" }}><Database size={20} /></div>
                        <h3>Database Controller</h3>
                      </div>
                      <p>Manage MariaDB virtual users and grants. Generate isolated SQL roles and secure phpMyAdmin logins.</p>
                    </div>
                    {/* Visual Mockup */}
                    <div className="glass-panel" style={{ background: "rgba(0,0,0,0.4)", padding: "0.8rem", borderRadius: "8px", border: "1px solid rgba(255,255,255,0.05)", display: "flex", justifyContent: "space-between", fontSize: "0.75rem" }}>
                      <span style={{ color: "#fff" }}>db_wordpress_3</span>
                      <span style={{ color: "var(--color-emerald)" }}>GRANT ALL PRIVILEGES</span>
                      <span style={{ color: "var(--text-muted)" }}>wp_user_99</span>
                    </div>
                  </div>
                </div>
              </>
            )}
          </motion.div>

        </div>

      </div>
    </section>
  );
}
