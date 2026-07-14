"use client";

import React from "react";
import { Server, Layers, Cpu, CheckCircle } from "lucide-react";
import { motion } from "framer-motion";

export default function Architecture() {
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
    <section id="architecture" className="arch-section" style={{ overflow: "hidden" }}>
      <div className="container">
        
        {/* Title Header */}
        <motion.div 
          initial={{ opacity: 0, y: 30 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true, margin: "-100px" }}
          transition={{ duration: 0.8, ease: easeTransition }}
          className="section-title-wrapper"
        >
          <span className="badge badge-indigo">SYSTEM ARCHITECTURE</span>
          <h2 className="section-title">Decoupled Three-Layer Core</h2>
          <p className="section-subtitle">
            CypherPanel is designed from the ground up for massive scaling. We split the control panel interface, central controller, and localized server daemons.
          </p>
        </motion.div>

        {/* Pipeline Flex Flow */}
        <div className="arch-flex-container">
          
          {/* Card 1: CypherCore */}
          <div className="arch-card-wrapper">
            <motion.div 
              initial={{ opacity: 0, y: 40 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true, margin: "-100px" }}
              transition={{ duration: 0.8, delay: 0.1, ease: easeTransition }}
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ height: "100%" }}
            >
              <div className="mouse-glow-card-content arch-card" style={{ height: "100%" }}>
                <div className="arch-icon-box" style={{ background: "rgba(99, 102, 241, 0.1)", color: "var(--color-indigo)" }}>
                  <Layers size={24} />
                </div>
                <h3 className="arch-title">1. CypherCore</h3>
                <p style={{ fontSize: "0.925rem" }}>
                  The centralized control plane controller. It runs the central API, manages schedules, coordinates queues, and handles databases.
                </p>
                <ul className="arch-list">
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-indigo)" style={{ marginTop: "2px" }} />
                    <span>Go-native REST/gRPC stateless API</span>
                  </li>
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-indigo)" style={{ marginTop: "2px" }} />
                    <span>NATS JetStream event queueing</span>
                  </li>
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-indigo)" style={{ marginTop: "2px" }} />
                    <span>PostgreSQL database & Redis caches</span>
                  </li>
                </ul>
              </div>
            </motion.div>
          </div>

          {/* Animated Pipeline Connector 1 */}
          <div className="arch-connector">
            <div className="arch-connector-line" />
            <div className="arch-connector-dot" />
          </div>

          {/* Card 2: CypherUI */}
          <div className="arch-card-wrapper">
            <motion.div 
              initial={{ opacity: 0, y: 40 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true, margin: "-100px" }}
              transition={{ duration: 0.8, delay: 0.2, ease: easeTransition }}
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ height: "100%" }}
            >
              <div className="mouse-glow-card-content arch-card" style={{ height: "100%" }}>
                <div className="arch-icon-box" style={{ background: "rgba(139, 92, 246, 0.1)", color: "var(--color-violet)" }}>
                  <Server size={24} />
                </div>
                <h3 className="arch-title">2. CypherUI</h3>
                <p style={{ fontSize: "0.925rem" }}>
                  The management interface. Designed to look and feel like premium modern SaaS, replacing dated, slow page-reload panels.
                </p>
                <ul className="arch-list">
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-violet)" style={{ marginTop: "2px" }} />
                    <span>Next.js App Router frontends</span>
                  </li>
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-violet)" style={{ marginTop: "2px" }} />
                    <span>Ctrl+K fuzzy Command Palette</span>
                  </li>
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-violet)" style={{ marginTop: "2px" }} />
                    <span>White-labeling & Multi-tenant designs</span>
                  </li>
                </ul>
              </div>
            </motion.div>
          </div>

          {/* Animated Pipeline Connector 2 */}
          <div className="arch-connector">
            <div className="arch-connector-line" />
            <div className="arch-connector-dot" />
          </div>

          {/* Card 3: CypherAgent */}
          <div className="arch-card-wrapper">
            <motion.div 
              initial={{ opacity: 0, y: 40 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true, margin: "-100px" }}
              transition={{ duration: 0.8, delay: 0.3, ease: easeTransition }}
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ height: "100%" }}
            >
              <div className="mouse-glow-card-content arch-card" style={{ height: "100%" }}>
                <div className="arch-icon-box" style={{ background: "rgba(16, 185, 129, 0.1)", color: "var(--color-emerald)" }}>
                  <Cpu size={24} />
                </div>
                <h3 className="arch-title">3. CypherAgent</h3>
                <p style={{ fontSize: "0.925rem" }}>
                  A lightweight daemon running locally on each managed machine, receiving operations from CypherCore to execute configuration changes.
                </p>
                <ul className="arch-list">
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-emerald)" style={{ marginTop: "2px" }} />
                    <span>Under 50MB idle RSS RAM footprint</span>
                  </li>
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-emerald)" style={{ marginTop: "2px" }} />
                    <span>cgroups v2 host user isolation slices</span>
                  </li>
                  <li className="arch-list-item">
                    <CheckCircle size={14} color="var(--color-emerald)" style={{ marginTop: "2px" }} />
                    <span>Local restic backups & ACME Lego</span>
                  </li>
                </ul>
              </div>
            </motion.div>
          </div>

        </div>

      </div>
    </section>
  );
}
