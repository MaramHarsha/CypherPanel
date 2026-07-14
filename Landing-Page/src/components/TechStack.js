"use client";

import React from "react";
import { Code, Globe, Server, Layers, Cpu, Database, Shield, Zap } from "lucide-react";
import { motion } from "framer-motion";

export default function TechStack() {
  const handleMouseMove = (e) => {
    const card = e.currentTarget;
    const rect = card.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    card.style.setProperty("--mouse-x", `${x}px`);
    card.style.setProperty("--mouse-y", `${y}px`);
  };

  const techItems = [
    {
      icon: <Code size={24} />,
      name: "Go (Golang)",
      role: "System-level Core Language"
    },
    {
      icon: <Globe size={24} />,
      name: "Next.js",
      role: "App Router Frontend"
    },
    {
      icon: <Server size={24} />,
      name: "Gin Gonic",
      role: "RESTful API Engine"
    },
    {
      icon: <Layers size={24} />,
      name: "gRPC & mTLS",
      role: "Secure Daemon Comms"
    },
    {
      icon: <Zap size={24} />,
      name: "NATS JetStream",
      role: "Low-footprint Task Queue"
    },
    {
      icon: <Database size={24} />,
      name: "PostgreSQL",
      role: "Configuration Registry"
    },
    {
      icon: <Database size={24} style={{ color: "var(--color-indigo)" }} />,
      name: "Redis",
      role: "Rate Limits & Sessions"
    },
    {
      icon: <Cpu size={24} />,
      name: "cgroups v2",
      role: "Linux User Sandboxing"
    }
  ];

  const easeTransition = [0.16, 1, 0.3, 1];

  return (
    <section id="techstack" className="tech-section" style={{ overflow: "hidden" }}>
      <div className="container">
        
        {/* Animated Title Header */}
        <motion.div 
          initial={{ opacity: 0, y: 30 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true, margin: "-100px" }}
          transition={{ duration: 0.8, ease: easeTransition }}
          className="section-title-wrapper"
        >
          <span className="badge badge-indigo">ENGINEERED STACK</span>
          <h2 className="section-title">Built for Modern System Engineers</h2>
          <p className="section-subtitle">
            Say goodbye to dated Perl scripts and bloated Erlang servers. CypherPanel is compiled for performance and efficiency.
          </p>
        </motion.div>

        <div className="tech-grid">
          {techItems.map((tech, idx) => (
            <motion.div 
              key={idx}
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true, margin: "-50px" }}
              transition={{ duration: 0.7, delay: (idx % 4) * 0.08, ease: easeTransition }}
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
            >
              <div className="mouse-glow-card-content tech-card">
                <div className="tech-icon">
                  {tech.icon}
                </div>
                <h4 className="tech-name">{tech.name}</h4>
                <span className="tech-role">{tech.role}</span>
              </div>
            </motion.div>
          ))}
        </div>

      </div>
    </section>
  );
}
