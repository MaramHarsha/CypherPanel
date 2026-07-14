"use client";

import React from "react";
import { Server } from "lucide-react";

export default function Navbar() {
  return (
    <nav className="navbar">
      <div className="navbar-container">
        <a href="#" className="logo">
          <div className="logo-icon">
            <Server size={18} color="#fff" />
          </div>
          CypherPanel
        </a>

        <ul className="nav-links">
          <li>
            <a href="#features" className="nav-link">
              Features
            </a>
          </li>
          <li>
            <a href="#architecture" className="nav-link">
              Architecture
            </a>
          </li>
          <li>
            <a href="#differentiators" className="nav-link">
              Differentiators
            </a>
          </li>
          <li>
            <a href="#techstack" className="nav-link">
              Tech Stack
            </a>
          </li>
          <li>
            <a href="#install" className="nav-link">
              Install
            </a>
          </li>
        </ul>

        <div className="nav-actions">
          <a
            href="https://github.com"
            target="_blank"
            rel="noopener noreferrer"
            className="btn btn-secondary"
            style={{ padding: "0.5rem 1rem", fontSize: "0.875rem" }}
          >
            <svg viewBox="0 0 24 24" width="16" height="16" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round">
              <path d="M9 19c-5 1.5-5-2.5-7-3m14 6v-3.87a3.37 3.37 0 0 0-.94-2.61c3.14-.35 6.44-1.54 6.44-7A5.44 5.44 0 0 0 20 4.77 5.07 5.07 0 0 0 19.91 1S18.73.65 16 2.48a13.38 13.38 0 0 0-7 0C6.27.65 5.09 1 5.09 1A5.07 5.07 0 0 0 5 4.77a5.44 5.44 0 0 0-1.5 3.78c0 5.42 3.3 6.61 6.44 7A3.37 3.37 0 0 0 9 18.13V22"></path>
            </svg>
            <span>GitHub</span>
          </a>
          <a href="#install" className="btn btn-primary" style={{ padding: "0.5rem 1rem", fontSize: "0.875rem" }}>
            Get Started
          </a>
        </div>
      </div>
    </nav>
  );
}
