"use client";

import React, { useState, useEffect, useRef } from "react";
import * as THREE from "three";
import { Terminal as TerminalIcon, Copy, Check, Shield, Cpu, Database, HardDrive, ArrowRight, Layout, Server, Settings, Globe, Mail, Activity } from "lucide-react";
import { motion, useScroll, useTransform } from "framer-motion";

export default function Hero() {
  const [typedCommand, setTypedCommand] = useState("");
  const [visibleOutputs, setVisibleOutputs] = useState([]);
  const [animationStep, setAnimationStep] = useState(0); 
  const [copied, setCopied] = useState(false);
  const [secureMode, setSecureMode] = useState(false);

  const canvasRef = useRef(null);
  const containerRef = useRef(null);
  const mockupRef = useRef(null);

  const installCmd = "curl -fsSL https://cypherpanel.org/install.sh | bash";
  
  const outputs = [
    { text: "[info] Checking system requirements...", color: "#94a3b8", delay: 500 },
    { text: "[info] OS detected: Rocky Linux 9.4", color: "#94a3b8", delay: 300 },
    { text: "[info] Memory check passed: 2.0GB RAM", color: "#94a3b8", delay: 300 },
    { text: "[info] Bootstrapping CypherCore & CypherAgent...", color: "#c084fc", delay: 500 },
    { text: "[success] CypherPanel installed successfully!", color: "#10b981", delay: 700, weight: "600" }
  ];

  // Mouse move tracker for 3D parallax tilt
  const mouse = useRef({ x: 0, y: 0 });
  const handleMouseMove = (e) => {
    if (!containerRef.current) return;
    const rect = containerRef.current.getBoundingClientRect();
    mouse.current.x = (e.clientX - rect.left) / rect.width - 0.5;
    mouse.current.y = (e.clientY - rect.top) / rect.height - 0.5;
  };

  // Scroll-driven 3D Perspective Tilt for the Dashboard Mockup
  const { scrollYProgress } = useScroll({
    target: mockupRef,
    offset: ["start end", "end start"]
  });

  const rotateX = useTransform(scrollYProgress, [0, 0.4], [18, 0]);
  const scale = useTransform(scrollYProgress, [0, 0.4], [0.92, 1]);
  const opacity = useTransform(scrollYProgress, [0, 0.3], [0.4, 1]);

  // Terminal Typing logic
  useEffect(() => {
    let timer;
    if (animationStep === 0) {
      if (typedCommand.length < installCmd.length) {
        timer = setTimeout(() => {
          setTypedCommand(installCmd.substring(0, typedCommand.length + 1));
        }, 35 + Math.random() * 30);
      } else {
        timer = setTimeout(() => setAnimationStep(1), 600);
      }
    } else if (animationStep <= outputs.length) {
      const currentOutput = outputs[animationStep - 1];
      timer = setTimeout(() => {
        setVisibleOutputs(prev => [...prev, currentOutput]);
        setAnimationStep(prev => prev + 1);
      }, currentOutput.delay);
    } else {
      timer = setTimeout(() => {
        setTypedCommand("");
        setVisibleOutputs([]);
        setAnimationStep(0);
      }, 7000);
    }
    return () => clearTimeout(timer);
  }, [typedCommand, animationStep]);

  // Three.js WebGL Scene setup for Hero Globe
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    let width = canvas.parentElement.clientWidth;
    let height = canvas.parentElement.clientHeight || 450;

    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(45, width / height, 0.1, 100);
    camera.position.z = 8.5;

    const renderer = new THREE.WebGLRenderer({
      canvas,
      antialias: true,
      alpha: true
    });
    renderer.setSize(width, height);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));

    const networkGroup = new THREE.Group();
    scene.add(networkGroup);

    // Node particle points
    const maxNodes = 140;
    const radius = 2.8;
    const basePositions = new Float32Array(maxNodes * 3);
    const currentPositions = new Float32Array(maxNodes * 3);

    for (let i = 0; i < maxNodes; i++) {
      const u = Math.random();
      const v = Math.random();
      const theta = u * 2.0 * Math.PI;
      const phi = Math.acos(2.0 * v - 1.0);
      
      const x = radius * Math.sin(phi) * Math.cos(theta);
      const y = radius * Math.sin(phi) * Math.sin(theta);
      const z = radius * Math.cos(phi);

      basePositions[i * 3] = x;
      basePositions[i * 3 + 1] = y;
      basePositions[i * 3 + 2] = z;

      currentPositions[i * 3] = x;
      currentPositions[i * 3 + 1] = y;
      currentPositions[i * 3 + 2] = z;
    }

    const nodesGeometry = new THREE.BufferGeometry();
    nodesGeometry.setAttribute("position", new THREE.BufferAttribute(currentPositions, 3));

    const pointsMaterial = new THREE.PointsMaterial({
      size: 0.1,
      color: 0x6366f1,
      transparent: true,
      opacity: 0.8
    });

    const nodePoints = new THREE.Points(nodesGeometry, pointsMaterial);
    networkGroup.add(nodePoints);

    // Line segments
    const lineIndices = [];
    const maxDistance = 1.35;

    for (let i = 0; i < maxNodes; i++) {
      for (let j = i + 1; j < maxNodes; j++) {
        const dx = basePositions[i * 3] - basePositions[j * 3];
        const dy = basePositions[i * 3 + 1] - basePositions[j * 3 + 1];
        const dz = basePositions[i * 3 + 2] - basePositions[j * 3 + 2];
        const dist = Math.sqrt(dx * dx + dy * dy + dz * dz);
        if (dist < maxDistance) {
          lineIndices.push(i, j);
        }
      }
    }

    const linesGeometry = new THREE.BufferGeometry();
    linesGeometry.setAttribute("position", new THREE.BufferAttribute(currentPositions, 3));
    linesGeometry.setIndex(lineIndices);

    const linesMaterial = new THREE.LineBasicMaterial({
      color: 0x8b5cf6,
      transparent: true,
      opacity: 0.22
    });

    const networkLines = new THREE.LineSegments(linesGeometry, linesMaterial);
    networkGroup.add(networkLines);

    // Lighting
    const ambientLight = new THREE.AmbientLight(0xffffff, 0.45);
    scene.add(ambientLight);

    const dirLight = new THREE.DirectionalLight(0xffffff, 0.8);
    dirLight.position.set(5, 5, 5);
    scene.add(dirLight);

    // Scroll tracker
    let scrollRatio = 0;
    const handleScroll = () => {
      const scrollY = window.scrollY;
      const docHeight = document.documentElement.scrollHeight - window.innerHeight;
      scrollRatio = docHeight > 0 ? scrollY / docHeight : 0;
    };
    window.addEventListener("scroll", handleScroll);

    // Resize
    const handleResize = () => {
      if (!canvas || !canvas.parentElement) return;
      width = canvas.parentElement.clientWidth;
      height = canvas.parentElement.clientHeight || 450;
      camera.aspect = width / height;
      camera.updateProjectionMatrix();
      renderer.setSize(width, height);
    };
    window.addEventListener("resize", handleResize);

    const clock = new THREE.Clock();

    const animate = () => {
      const time = clock.getElapsedTime();

      // Rotation & parallax tilt
      networkGroup.rotation.y = time * 0.08 + scrollRatio * Math.PI * 1.2 + mouse.current.x * 0.4;
      networkGroup.rotation.x = scrollRatio * Math.PI * 0.4 + mouse.current.y * 0.4;

      // Jitter
      const positions = nodesGeometry.attributes.position.array;
      for (let i = 0; i < maxNodes; i++) {
        const jitterAmp = secureMode ? 0.02 : 0.04;
        positions[i * 3] = basePositions[i * 3] + Math.sin(time * 1.2 + i) * jitterAmp;
        positions[i * 3 + 1] = basePositions[i * 3 + 1] + Math.cos(time * 1.2 + i) * jitterAmp;
        positions[i * 3 + 2] = basePositions[i * 3 + 2] + Math.sin(time * 0.9 + i) * jitterAmp;
      }
      nodesGeometry.attributes.position.needsUpdate = true;
      linesGeometry.attributes.position.needsUpdate = true;

      renderer.render(scene, camera);
      animationFrameId = requestAnimationFrame(animate);
    };

    let animationFrameId = requestAnimationFrame(animate);

    return () => {
      cancelAnimationFrame(animationFrameId);
      window.removeEventListener("scroll", handleScroll);
      window.removeEventListener("resize", handleResize);
      renderer.dispose();
      pointsMaterial.dispose();
      linesMaterial.dispose();
      nodesGeometry.dispose();
      linesGeometry.dispose();
    };
  }, [secureMode]);

  const copyToClipboard = () => {
    navigator.clipboard.writeText(installCmd);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const easeTransition = [0.16, 1, 0.3, 1];

  return (
    <section className="hero" ref={containerRef} onMouseMove={handleMouseMove} style={{ position: "relative", overflow: "hidden", paddingBottom: "10rem" }}>
      {/* Dynamic Background Spotlight */}
      <div style={{
        position: "absolute",
        top: "20%",
        left: "50%",
        transform: "translate(-50%, -50%)",
        width: "900px",
        height: "500px",
        background: "radial-gradient(circle, rgba(99, 102, 241, 0.08) 0%, transparent 70%)",
        filter: "blur(60px)",
        pointerEvents: "none",
        zIndex: 0
      }} />

      <div className="container" style={{ position: "relative", zIndex: 10 }}>
        
        {/* Split Section Layout: 3D Scene + Hero content */}
        <div className="arch-grid" style={{ gridTemplateColumns: "1.1fr 0.9fr", gap: "4rem", alignItems: "center", marginBottom: "7rem" }}>
          
          {/* Left Column: Typography, Copy block & CTAs */}
          <div style={{ textAlign: "left", display: "flex", flexDirection: "column", alignItems: "flex-start" }}>
            
            <motion.div 
              initial={{ opacity: 0, y: 15 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.6, ease: easeTransition }}
              className="hero-badge-container"
              style={{ marginLeft: "0", marginRight: "0" }}
            >
              <span className="badge badge-indigo">Apache-2.0 Open Source Challenger</span>
            </motion.div>

            <motion.h1 
              initial={{ opacity: 0, y: 25 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.8, delay: 0.1, ease: easeTransition }}
              className="hero-title text-gradient"
              style={{ marginLeft: "0", marginRight: "0", textAlign: "left", fontSize: "clamp(2.5rem, 6vw, 3.8rem)", lineHeight: "1.1" }}
            >
              The Modern, Secure, Open-Source <br />
              <span className="text-gradient-purple">cPanel & WHM Alternative</span>
            </motion.h1>

            <motion.p 
              initial={{ opacity: 0, y: 25 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.8, delay: 0.2, ease: easeTransition }}
              className="hero-subtitle"
              style={{ marginLeft: "0", marginRight: "0", textAlign: "left", fontSize: "1.15rem", marginBottom: "2.5rem" }}
            >
              An API-first, self-hosted hosting control plane built for agencies, VPS administrators, and web developers. Run secure hosting fleets with cgroups sandboxing.
            </motion.p>

            {/* In-Hero Live Terminal Copy Box */}
            <motion.div
              initial={{ opacity: 0, y: 20 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.8, delay: 0.25, ease: easeTransition }}
              style={{ width: "100%", maxWidth: "520px", marginBottom: "2.5rem" }}
            >
              <div 
                className="installer-cmd-box" 
                style={{ 
                  background: "rgba(3, 4, 12, 0.6)", 
                  backdropFilter: "blur(12px)", 
                  border: "1px solid rgba(255,255,255,0.08)",
                  padding: "0.85rem 1.25rem"
                }}
              >
                <div style={{ display: "flex", alignItems: "center", gap: "0.6rem", overflow: "hidden" }}>
                  <TerminalIcon size={16} color="var(--color-emerald)" />
                  <div className="installer-cmd" style={{ fontSize: "0.85rem" }}>{installCmd}</div>
                </div>
                <button className="installer-copy-btn" onClick={copyToClipboard} style={{ padding: "0.4rem 0.8rem", fontSize: "0.8rem" }}>
                  {copied ? <Check size={12} color="#10b981" /> : <Copy size={12} />}
                </button>
              </div>
            </motion.div>

            <motion.div 
              initial={{ opacity: 0, y: 25 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.8, delay: 0.3, ease: easeTransition }}
              className="hero-buttons"
              style={{ marginLeft: "0", marginRight: "0" }}
            >
              <a href="#install" className="btn btn-primary">
                <span>Install Now</span>
                <ArrowRight size={16} />
              </a>
              <a href="#features" className="btn btn-secondary">
                <span>Explore Features</span>
              </a>
            </motion.div>

          </div>

          {/* Right Column: WebGL Interactive Scene */}
          <motion.div 
            initial={{ opacity: 0, scale: 0.95 }}
            animate={{ opacity: 1, scale: 1 }}
            transition={{ duration: 1, delay: 0.2, ease: easeTransition }}
            style={{ 
              position: "relative", 
              width: "100%", 
              height: "450px", 
              display: "flex", 
              alignItems: "center", 
              justifyContent: "center" 
            }}
          >
            {/* Custom glowing sphere behind globe */}
            <div style={{
              position: "absolute",
              width: "60%",
              height: "60%",
              borderRadius: "50%",
              background: secureMode ? "radial-gradient(circle, rgba(16, 185, 129, 0.06) 0%, transparent 70%)" : "radial-gradient(circle, rgba(99, 102, 241, 0.08) 0%, transparent 70%)",
              filter: "blur(20px)",
              pointerEvents: "none",
              zIndex: 0
            }} />
            <canvas ref={canvasRef} style={{ width: "100%", height: "100%", zIndex: 10 }} />
          </motion.div>

        </div>

        {/* Scroll-Driven 3D Perspective Dashboard Preview */}
        <div ref={mockupRef} style={{ perspective: "1200px" }}>
          <motion.div 
            style={{ 
              rotateX, 
              scale, 
              opacity,
              transformStyle: "preserve-3d"
            }}
            className="dashboard-preview-wrapper"
          >
            <div className="dashboard-preview glass-panel">
              
              {/* Mockup Sidebar */}
              <aside className="dash-sidebar">
                <div className="dash-nav">
                  <div className="logo" style={{ fontSize: "1.05rem", marginBottom: "1.5rem" }}>
                    <div className="logo-icon" style={{ width: "24px", height: "24px", borderRadius: "5px" }}>
                      <Server size={12} color="#fff" />
                    </div>
                    <span>CypherPanel</span>
                  </div>
                  
                  <div className="dash-nav-item active">
                    <Layout size={14} />
                    <span>Dashboard</span>
                  </div>
                  <div className="dash-nav-item">
                    <Server size={14} />
                    <span>Server Nodes</span>
                  </div>
                  <div className="dash-nav-item">
                    <Globe size={14} />
                    <span>Domains</span>
                  </div>
                  <div className="dash-nav-item">
                    <Database size={14} />
                    <span>Databases</span>
                  </div>
                  <div className="dash-nav-item">
                    <Mail size={14} />
                    <span>Email Accounts</span>
                  </div>
                  <div className="dash-nav-item">
                    <Shield size={14} />
                    <span>Security & WAF</span>
                  </div>
                </div>
                
                <div className="dash-nav">
                  <div className="dash-nav-item">
                    <Settings size={14} />
                    <span>Settings</span>
                  </div>
                </div>
              </aside>

              {/* Mockup Main Content */}
              <main className="dash-main">
                <div className="dash-header">
                  <div>
                    <h3 className="dash-title">node-primary-rocky</h3>
                    <p style={{ fontSize: "0.75rem", color: "var(--text-muted)", marginTop: "2px" }}>
                      Rocky Linux 9.4 • IP: 198.51.100.42 • Agent: Connected
                    </p>
                  </div>
                  <span className="badge badge-emerald">Online</span>
                </div>

                {/* Server Stats Grid */}
                <div className="dash-grid">
                  
                  <div className="mouse-glow-card">
                    <div className="mouse-glow-card-content dash-card">
                      <div className="dash-card-title">CPU Load</div>
                      <div className="dash-card-value">12.4%</div>
                      <div className="progress-bar-bg">
                        <div className="progress-bar emerald" style={{ width: "12.4%" }}></div>
                      </div>
                      <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>cgroup slice cap: 100%</span>
                    </div>
                  </div>

                  <div className="mouse-glow-card">
                    <div className="mouse-glow-card-content dash-card">
                      <div className="dash-card-title">Memory Allocation</div>
                      <div className="dash-card-value">1.13 GB / 4 GB</div>
                      <div className="progress-bar-bg">
                        <div className="progress-bar indigo" style={{ width: "28%" }}></div>
                      </div>
                      <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>Agent Footprint: 22MB RSS</span>
                    </div>
                  </div>

                  <div className="mouse-glow-card">
                    <div className="mouse-glow-card-content dash-card">
                      <div className="dash-card-title">Disk Storage</div>
                      <div className="dash-card-value">42.8 GB / 120 GB</div>
                      <div className="progress-bar-bg">
                        <div className="progress-bar amber" style={{ width: "35.6%" }}></div>
                      </div>
                      <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>restic backups enabled</span>
                    </div>
                  </div>

                </div>

                {/* Active Websites Table */}
                <div className="mouse-glow-card" style={{ flex: 1 }}>
                  <div className="mouse-glow-card-content dash-list-card">
                    <div className="dash-list-header">
                      <span>Managed Virtual Hosts</span>
                      <span style={{ fontSize: "0.75rem", fontWeight: "normal" }}>3 Active Domains</span>
                    </div>
                    
                    <div className="dash-list-item">
                      <div style={{ display: "flex", flexDirection: "column" }}>
                        <span style={{ fontWeight: "600", color: "#fff" }}>example.com</span>
                        <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>PHP 8.3 • Nginx FPM</span>
                      </div>
                      <span className="badge badge-indigo">SSL Active</span>
                      <div style={{ textAlign: "right" }}>
                        <span style={{ fontWeight: "500", color: "#fff", display: "block", fontSize: "0.85rem" }}>14.2 GB</span>
                        <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>Bandwidth</span>
                      </div>
                    </div>

                    <div className="dash-list-item">
                      <div style={{ display: "flex", flexDirection: "column" }}>
                        <span style={{ fontWeight: "600", color: "#fff" }}>api.example.com</span>
                        <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>Node.js 20 • Reverse Proxy</span>
                      </div>
                      <span className="badge badge-indigo">SSL Active</span>
                      <div style={{ textAlign: "right" }}>
                        <span style={{ fontWeight: "500", color: "#fff", display: "block", fontSize: "0.85rem" }}>8.7 GB</span>
                        <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>Bandwidth</span>
                      </div>
                    </div>

                    <div className="dash-list-item">
                      <div style={{ display: "flex", flexDirection: "column" }}>
                        <span style={{ fontWeight: "600", color: "#fff" }}>staging.app.dev</span>
                        <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>PHP 8.2 • Isolated Sandbox</span>
                      </div>
                      <span className="badge badge-amber">Self-Signed</span>
                      <div style={{ textAlign: "right" }}>
                        <span style={{ fontWeight: "500", color: "#fff", display: "block", fontSize: "0.85rem" }}>1.2 GB</span>
                        <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>Bandwidth</span>
                      </div>
                    </div>
                  </div>
                </div>

              </main>

            </div>
          </motion.div>
        </div>

      </div>
    </section>
  );
}
