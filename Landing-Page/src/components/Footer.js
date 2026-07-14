"use client";

import React, { useState } from "react";
import { Server, Shield, ArrowRight, Check } from "lucide-react";

export default function Footer() {
  const [email, setEmail] = useState("");
  const [subscribed, setSubscribed] = useState(false);

  const handleSubscribe = (e) => {
    e.preventDefault();
    if (!email) return;
    setSubscribed(true);
    setTimeout(() => {
      setEmail("");
      setSubscribed(false);
    }, 3000);
  };

  return (
    <footer className="footer" style={{ position: "relative", overflow: "hidden", borderTop: "1px solid rgba(255,255,255,0.06)" }}>
      {/* Background ambient lighting spotlight */}
      <div style={{
        position: "absolute",
        bottom: "-100px",
        left: "50%",
        transform: "translateX(-50%)",
        width: "700px",
        height: "350px",
        background: "radial-gradient(circle, rgba(99, 102, 241, 0.08) 0%, transparent 80%)",
        filter: "blur(60px)",
        pointerEvents: "none",
        zIndex: 0
      }} />

      <div className="container" style={{ position: "relative", zIndex: 10 }}>
        
        {/* Footer Top Grid */}
        <div className="footer-grid">
          
          <div className="footer-brand" style={{ gap: "1.5rem" }}>
            <div className="logo" style={{ fontSize: "1.35rem" }}>
              <div className="logo-icon" style={{ width: "30px", height: "30px", borderRadius: "6px" }}>
                <Server size={14} color="#fff" />
              </div>
              <span>CypherPanel</span>
            </div>
            <p className="footer-desc" style={{ fontSize: "0.95rem", lineHeight: "1.6", color: "var(--text-secondary)" }}>
              The cloud-native, open-source control plane engineered for speed, host isolation, and massive node fleet management.
            </p>
            
            {/* Integrated Subscription Input */}
            <form onSubmit={handleSubscribe} style={{ position: "relative", width: "100%", maxWidth: "320px" }}>
              <div style={{
                display: "flex",
                alignItems: "center",
                background: "rgba(255, 255, 255, 0.02)",
                border: "1px solid rgba(255, 255, 255, 0.08)",
                borderRadius: "99px",
                padding: "4px 4px 4px 16px",
                boxShadow: "inset 0 1px 2px rgba(0,0,0,0.8)"
              }}>
                <input 
                  type="email" 
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="Subscribe to updates..."
                  style={{
                    background: "none",
                    border: "none",
                    outline: "none",
                    color: "#fff",
                    fontSize: "0.85rem",
                    width: "100%",
                    fontFamily: "var(--font-sans)"
                  }}
                  disabled={subscribed}
                />
                <button 
                  type="submit"
                  style={{
                    background: subscribed ? "var(--color-emerald)" : "var(--color-indigo)",
                    border: "none",
                    outline: "none",
                    width: "32px",
                    height: "32px",
                    borderRadius: "50%",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    color: "#fff",
                    cursor: "pointer",
                    transition: "all 0.3s"
                  }}
                >
                  {subscribed ? <Check size={14} /> : <ArrowRight size={14} />}
                </button>
              </div>
              {subscribed && (
                <span style={{ fontSize: "0.75rem", color: "var(--color-emerald)", position: "absolute", bottom: "-20px", left: "12px" }}>
                  Thanks for subscribing!
                </span>
              )}
            </form>
          </div>

          <div style={{ display: "flex", flexDirection: "column", alignItems: "flex-start", marginLeft: "auto" }}>
            <h4 className="footer-links-title">Product</h4>
            <ul className="footer-links-list">
              <li>
                <a href="#features" className="footer-link">Features</a>
              </li>
              <li>
                <a href="#architecture" className="footer-link">Architecture</a>
              </li>
              <li>
                <a href="#differentiators" className="footer-link">Differentiators</a>
              </li>
              <li>
                <a href="#techstack" className="footer-link">Tech Stack</a>
              </li>
            </ul>
          </div>

          <div style={{ display: "flex", flexDirection: "column", alignItems: "flex-start", marginLeft: "auto" }}>
            <h4 className="footer-links-title">Developers</h4>
            <ul className="footer-links-list">
              <li>
                <a href="https://github.com" className="footer-link" target="_blank" rel="noopener noreferrer">GitHub Project</a>
              </li>
              <li>
                <a href="https://github.com" className="footer-link" target="_blank" rel="noopener noreferrer">Documentation</a>
              </li>
              <li>
                <a href="https://github.com" className="footer-link" target="_blank" rel="noopener noreferrer">Installer CLI</a>
              </li>
            </ul>
          </div>

        </div>

        {/* Giant outline text signature */}
        <div style={{
          userSelect: "none",
          textAlign: "center",
          marginTop: "5rem",
          marginBottom: "2rem",
          overflow: "hidden"
        }}>
          <h1 style={{
            fontSize: "clamp(3.5rem, 15vw, 11rem)",
            fontWeight: "900",
            fontFamily: "var(--font-heading)",
            lineHeight: "0.8",
            margin: "0",
            textTransform: "uppercase",
            letterSpacing: "-0.04em",
            background: "linear-gradient(180deg, rgba(255,255,255,0.06) 0%, rgba(255,255,255,0) 100%)",
            WebkitBackgroundClip: "text",
            WebkitTextFillColor: "transparent",
            backgroundClip: "text",
            display: "inline-block"
          }}>
            CYPHER
          </h1>
        </div>

        {/* Footer Bottom */}
        <div className="footer-bottom">
          <div>
            &copy; {new Date().getFullYear()} CypherPanel Project. Released under Apache-2.0.
          </div>
          
          <div className="license-info">
            <Shield size={14} color="var(--color-indigo)" />
            <span>Open Source Repository</span>
          </div>
        </div>

      </div>
    </footer>
  );
}
