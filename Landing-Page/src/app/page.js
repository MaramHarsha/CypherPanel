"use client";

import React, { useState, useEffect, useRef } from "react";
import * as THREE from "three";
import { 
  Server, Terminal as TerminalIcon, Copy, Check, Shield, Cpu, Database, 
  Globe, Mail, Zap, GitBranch, ArrowRight, Layout, Settings, 
  Sliders, Play, ShieldAlert, Key, FolderOpen, RefreshCw
} from "lucide-react";
import { motion, AnimatePresence } from "framer-motion";
import Navbar from "@/components/Navbar";
import Footer from "@/components/Footer";

export default function Home() {
  // 1. Node Selector State
  const [selectedNode, setSelectedNode] = useState("us-east");
  const [nodeMetrics, setNodeMetrics] = useState({
    "us-east": { cpu: 14.2, ram: "1.18 GB / 4 GB", disk: "32.4 GB", status: "Online", color: 0x6366f1 },
    "eu-central": { cpu: 28.5, ram: "2.35 GB / 8 GB", disk: "112.8 GB", status: "Online", color: 0x8b5cf6 },
    "ap-south": { cpu: 8.1, ram: "0.82 GB / 4 GB", disk: "12.4 GB", status: "Online", color: 0x10b981 }
  });

  // 2. YAML Playground State
  const [configPhp, setConfigPhp] = useState("8.3");
  const [configSsl, setConfigSsl] = useState(true);
  const [configWaf, setConfigWaf] = useState(true);
  const [configHighlight, setConfigHighlight] = useState(null);

  // 3. CLI Terminal Simulation State
  const [typedCommand, setTypedCommand] = useState("");
  const [terminalLogs, setTerminalLogs] = useState([]);
  const [terminalStep, setTerminalStep] = useState(0); // 0 = ready, 1 = typing, 2 = installing, 3 = finished
  const [copied, setCopied] = useState(false);

  const installCmd = "curl -fsSL https://cypherpanel.org/install.sh | bash";
  const logsList = [
    "[info] Initializing rockylinux-9.4 environment...",
    "[info] Downloading lego SSL client & ACME scripts...",
    "[info] Binding Nginx upstream reverse proxies...",
    "[success] CypherAgent connected locally via secure mTLS!",
    "[success] Admin console available at: https://your-server-ip:8443"
  ];

  // Refs
  const canvasRef = useRef(null);
  const containerRef = useRef(null);

  // Mouse tilt tracking
  const mouse = useRef({ x: 0, y: 0 });
  const handleMouseMove = (e) => {
    const card = e.currentTarget;
    const rect = card.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    card.style.setProperty("--mouse-x", `${x}px`);
    card.style.setProperty("--mouse-y", `${y}px`);
    
    // WebGL tilt coords
    mouse.current.x = (e.clientX - rect.left) / rect.width - 0.5;
    mouse.current.y = (e.clientY - rect.top) / rect.height - 0.5;
  };

  // Three.js Interactive WebGL Globe logic
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    let width = canvas.parentElement.clientWidth;
    let height = canvas.parentElement.clientHeight || 360;

    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(45, width / height, 0.1, 100);
    camera.position.z = 7.5;

    const renderer = new THREE.WebGLRenderer({
      canvas,
      antialias: true,
      alpha: true
    });
    renderer.setSize(width, height);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));

    const networkGroup = new THREE.Group();
    scene.add(networkGroup);

    // Particle nodes
    const maxNodes = 100;
    const radius = 2.4;
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

    // Initial node color from selectedNode color map
    const activeColor = nodeMetrics[selectedNode].color;
    const pointsMaterial = new THREE.PointsMaterial({
      size: 0.12,
      color: activeColor,
      transparent: true,
      opacity: 0.85
    });

    const nodePoints = new THREE.Points(nodesGeometry, pointsMaterial);
    networkGroup.add(nodePoints);

    // Connection lines
    const lineIndices = [];
    const maxDistance = 1.3;
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
      color: activeColor,
      transparent: true,
      opacity: 0.22
    });

    const networkLines = new THREE.LineSegments(linesGeometry, linesMaterial);
    networkGroup.add(networkLines);

    // Light
    const ambientLight = new THREE.AmbientLight(0xffffff, 0.5);
    scene.add(ambientLight);

    const resizeHandler = () => {
      if (!canvas || !canvas.parentElement) return;
      width = canvas.parentElement.clientWidth;
      height = canvas.parentElement.clientHeight || 360;
      camera.aspect = width / height;
      camera.updateProjectionMatrix();
      renderer.setSize(width, height);
    };
    window.addEventListener("resize", resizeHandler);

    let animationFrameId;
    const clock = new THREE.Clock();

    const animate = () => {
      const time = clock.getElapsedTime();

      // Dynamic rotation speed based on active node
      const rotSpeed = selectedNode === "ap-south" ? 0.25 : selectedNode === "eu-central" ? 0.16 : 0.08;
      networkGroup.rotation.y = time * rotSpeed + mouse.current.x * 0.4;
      networkGroup.rotation.x = time * 0.04 + mouse.current.y * 0.4;

      // Jitter
      const positions = nodesGeometry.attributes.position.array;
      const amp = selectedNode === "eu-central" ? 0.06 : 0.03;
      for (let i = 0; i < maxNodes; i++) {
        positions[i * 3] = basePositions[i * 3] + Math.sin(time * 1.5 + i) * amp;
        positions[i * 3 + 1] = basePositions[i * 3 + 1] + Math.cos(time * 1.5 + i) * amp;
        positions[i * 3 + 2] = basePositions[i * 3 + 2] + Math.sin(time * 1.1 + i) * amp;
      }
      nodesGeometry.attributes.position.needsUpdate = true;
      linesGeometry.attributes.position.needsUpdate = true;

      renderer.render(scene, camera);
      animationFrameId = requestAnimationFrame(animate);
    };
    animate();

    return () => {
      cancelAnimationFrame(animationFrameId);
      window.removeEventListener("resize", resizeHandler);
      renderer.dispose();
      pointsMaterial.dispose();
      linesMaterial.dispose();
      nodesGeometry.dispose();
      linesGeometry.dispose();
    };
  }, [selectedNode]);

  // Terminal CLI Trigger Animation
  const runInstallDemo = () => {
    if (terminalStep !== 0) return;
    setTerminalStep(1);
    let cmdProgress = "";
    let idx = 0;
    
    const typingInterval = setInterval(() => {
      if (idx < installCmd.length) {
        cmdProgress += installCmd[idx];
        setTypedCommand(cmdProgress);
        idx++;
      } else {
        clearInterval(typingInterval);
        setTerminalStep(2);
        
        // Print logs
        let logIdx = 0;
        const logInterval = setInterval(() => {
          if (logIdx < logsList.length) {
            const currentLog = logsList[logIdx];
            setTerminalLogs(prev => [...prev, currentLog]);
            logIdx++;
          } else {
            clearInterval(logInterval);
            setTerminalStep(3);
          }
        }, 600);
      }
    }, 40);
  };

  const resetTerminal = () => {
    setTypedCommand("");
    setTerminalLogs([]);
    setTerminalStep(0);
  };

  const copyInstall = () => {
    navigator.clipboard.writeText(installCmd);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  // Trigger brief highlight animation in YAML panel on toggle
  const triggerHighlight = (key) => {
    setConfigHighlight(key);
    setTimeout(() => setConfigHighlight(null), 800);
  };

  const easeTransition = [0.16, 1, 0.3, 1];

  return (
    <>
      <Navbar />
      
      {/* 1. Immersive Console Hero Section */}
      <section className="console-hero" style={{ position: "relative", overflow: "hidden" }}>
        <div className="container">
          <motion.div 
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.8, ease: easeTransition }}
          >
            <span className="badge badge-indigo" style={{ marginBottom: "1.5rem" }}>API-First Control Plane v1.0.0</span>
            <h1 className="console-hero-title text-gradient">
              Observe. Configure. Secure.<br />
              <span className="text-gradient-purple">The Modern Hosting Workstation</span>
            </h1>
            <p className="console-hero-subtitle">
              Say goodbye to bloated legacy panels. CypherPanel orchestrates virtual hosts, mail servers, databases, and network records across isolated server clusters using declarative code configurations.
            </p>
          </motion.div>
        </div>
      </section>

      {/* 2. Interactive Console Bento Grid */}
      <section style={{ paddingBottom: "8rem" }}>
        <div className="container">
          
          <div className="console-container-grid">
            
            {/* CELL 1: WebGL Fleet Controller & Node Selector (Takes 8 cols) */}
            <div 
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove} 
              style={{ gridColumn: "span 8" }}
            >
              <div className="console-card" style={{ flexDirection: "column", gap: "2rem" }}>
                <div>
                  <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "0.5rem" }}>
                    <h3 style={{ fontSize: "1.4rem" }}>Global Fleet Controller</h3>
                    <span className="badge badge-emerald">Connected Agents</span>
                  </div>
                  <p style={{ color: "var(--text-secondary)", fontSize: "0.95rem" }}>
                    Select a localized server node below. The Three.js WebGL canvas visualizes latency fluctuations, task queue volumes, and secure mTLS sockets dynamically.
                  </p>
                </div>

                <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "2rem", alignItems: "center" }}>
                  
                  {/* Interactive selector */}
                  <div className="node-list-interactive">
                    <button 
                      onClick={() => setSelectedNode("us-east")} 
                      className={`node-list-item-btn ${selectedNode === "us-east" ? "active" : ""}`}
                    >
                      <div>
                        <strong style={{ display: "block" }}>US-East (Virginia)</strong>
                        <span style={{ fontSize: "0.75rem", color: "var(--text-muted)" }}>Rocky Linux 9.4 • Primary</span>
                      </div>
                      <span style={{ fontSize: "0.8rem", color: "var(--color-indigo)" }}>{nodeMetrics["us-east"].cpu}% CPU</span>
                    </button>

                    <button 
                      onClick={() => setSelectedNode("eu-central")} 
                      className={`node-list-item-btn ${selectedNode === "eu-central" ? "active" : ""}`}
                    >
                      <div>
                        <strong style={{ display: "block" }}>EU-Central (Frankfurt)</strong>
                        <span style={{ fontSize: "0.75rem", color: "var(--text-muted)" }}>Debian 12 • Backup Replica</span>
                      </div>
                      <span style={{ fontSize: "0.8rem", color: "var(--color-violet)" }}>{nodeMetrics["eu-central"].cpu}% CPU</span>
                    </button>

                    <button 
                      onClick={() => setSelectedNode("ap-south")} 
                      className={`node-list-item-btn ${selectedNode === "ap-south" ? "active" : ""}`}
                    >
                      <div>
                        <strong style={{ display: "block" }}>AP-South (Mumbai)</strong>
                        <span style={{ fontSize: "0.75rem", color: "var(--text-muted)" }}>Ubuntu 24.04 • Edge Cache</span>
                      </div>
                      <span style={{ fontSize: "0.8rem", color: "var(--color-emerald)" }}>{nodeMetrics["ap-south"].cpu}% CPU</span>
                    </button>
                  </div>

                  {/* 3D WebGL Canvas */}
                  <div style={{ position: "relative", width: "100%", height: "280px" }}>
                    <canvas ref={canvasRef} style={{ width: "100%", height: "100%" }} />
                  </div>

                </div>

                {/* Live Node Metrics bar */}
                <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: "1rem", borderTop: "1px solid rgba(255,255,255,0.06)", paddingTop: "1.25rem", fontSize: "0.85rem" }}>
                  <div>
                    <span style={{ color: "var(--text-muted)", display: "block", fontSize: "0.75rem" }}>RAM ALLOCATION</span>
                    <strong style={{ color: "#fff" }}>{nodeMetrics[selectedNode].ram}</strong>
                  </div>
                  <div>
                    <span style={{ color: "var(--text-muted)", display: "block", fontSize: "0.75rem" }}>DISK STORAGE</span>
                    <strong style={{ color: "#fff" }}>{nodeMetrics[selectedNode].disk}</strong>
                  </div>
                  <div>
                    <span style={{ color: "var(--text-muted)", display: "block", fontSize: "0.75rem" }}>DAEMON NODE STATE</span>
                    <strong style={{ color: "var(--color-emerald)" }}>{nodeMetrics[selectedNode].status}</strong>
                  </div>
                </div>

              </div>
            </div>

            {/* CELL 2: Declarative Nginx Router & Code Editor (Takes 4 cols) */}
            <div 
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ gridColumn: "span 4" }}
            >
              <div className="console-card" style={{ flexDirection: "column", gap: "1.5rem" }}>
                <div>
                  <h3 style={{ fontSize: "1.3rem", marginBottom: "0.5rem" }}>YAML Vhost Editor</h3>
                  <p style={{ color: "var(--text-secondary)", fontSize: "0.9rem" }}>
                    Toggle declarative configuration states and watch the virtual host engine compile immediately.
                  </p>
                </div>

                {/* Toggle controls */}
                <div style={{ display: "flex", flexDirection: "column", gap: "0.75rem" }}>
                  
                  {/* PHP Toggle */}
                  <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                    <span style={{ fontSize: "0.85rem", color: "var(--text-secondary)" }}>PHP FPM Version</span>
                    <div style={{ display: "flex", gap: "0.25rem", background: "rgba(255,255,255,0.02)", border: "1px solid var(--border-glass)", borderRadius: "99px", padding: "2px" }}>
                      {["8.2", "8.3", "8.4"].map(v => (
                        <button 
                          key={v}
                          onClick={() => { setConfigPhp(v); triggerHighlight("php"); }}
                          style={{
                            background: configPhp === v ? "var(--color-indigo)" : "none",
                            border: "none",
                            borderRadius: "99px",
                            color: "#fff",
                            fontSize: "0.75rem",
                            padding: "0.25rem 0.6rem",
                            cursor: "pointer"
                          }}
                        >
                          {v}
                        </button>
                      ))}
                    </div>
                  </div>

                  {/* SSL Toggle */}
                  <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                    <span style={{ fontSize: "0.85rem", color: "var(--text-secondary)" }}>Auto Let's Encrypt</span>
                    <button 
                      onClick={() => { setConfigSsl(!configSsl); triggerHighlight("ssl"); }}
                      className={`badge ${configSsl ? "badge-indigo" : "badge-rose"}`}
                      style={{ cursor: "pointer" }}
                    >
                      {configSsl ? "Enabled" : "Disabled"}
                    </button>
                  </div>

                  {/* WAF Toggle */}
                  <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                    <span style={{ fontSize: "0.85rem", color: "var(--text-secondary)" }}>WAF ModSecurity</span>
                    <button 
                      onClick={() => { setConfigWaf(!configWaf); triggerHighlight("waf"); }}
                      className={`badge ${configWaf ? "badge-emerald" : "badge-rose"}`}
                      style={{ cursor: "pointer" }}
                    >
                      {configWaf ? "Active" : "Disabled"}
                    </button>
                  </div>

                </div>

                {/* Syntax Highlighted editor */}
                <div style={{
                  background: "#030408",
                  border: "1px solid rgba(255,255,255,0.08)",
                  borderRadius: "10px",
                  padding: "1rem",
                  fontFamily: "var(--font-mono)",
                  fontSize: "0.75rem",
                  color: "#a5b4fc"
                }}>
                  <div><span style={{ color: "#f43f5e" }}>domain:</span> my-app.com</div>
                  <div><span style={{ color: "#f43f5e" }}>vhost:</span></div>
                  <div>&nbsp;&nbsp;engine: nginx</div>
                  <div className={configHighlight === "php" ? "yaml-line-active" : ""}>
                    &nbsp;&nbsp;php_version: <span style={{ color: "#fff" }}>"{configPhp}"</span>
                  </div>
                  <div className={configHighlight === "ssl" ? "yaml-line-active" : ""}>
                    &nbsp;&nbsp;ssl_acme: <span style={{ color: "#fff" }}>{configSsl ? "true" : "false"}</span>
                  </div>
                  <div className={configHighlight === "waf" ? "yaml-line-active" : ""}>
                    &nbsp;&nbsp;waf_ruleset: <span style={{ color: "#fff" }}>{configWaf ? "coreruleset" : "none"}</span>
                  </div>
                </div>
              </div>
            </div>

            {/* CELL 3: Command Line CLI terminal simulator (Takes 5 cols) */}
            <div 
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ gridColumn: "span 5" }}
            >
              <div className="console-card" style={{ flexDirection: "column", gap: "1.5rem" }}>
                <div>
                  <h3 style={{ fontSize: "1.3rem", marginBottom: "0.5rem" }}>Interactive Command CLI</h3>
                  <p style={{ color: "var(--text-secondary)", fontSize: "0.9rem" }}>
                    Run the CypherAgent controller installer locally. Click the action triggers to run or reset the simulation.
                  </p>
                </div>

                <div className="terminal-window">
                  <div className="terminal-header">
                    <div className="terminal-dots">
                      <span className="terminal-dot red"></span>
                      <span className="terminal-dot yellow"></span>
                      <span className="terminal-dot green"></span>
                    </div>
                    <span style={{ fontSize: "0.7rem", color: "var(--text-muted)" }}>installer.sh</span>
                  </div>
                  <div className="terminal-body" style={{ minHeight: "180px" }}>
                    <div>
                      <span className="terminal-prompt">root@server:~$</span>
                      <span>{typedCommand}</span>
                      {terminalStep === 1 && <span className="terminal-cursor" />}
                    </div>

                    {terminalLogs.map((log, idx) => (
                      <div key={idx} style={{ marginTop: "4px", color: log.includes("success") ? "var(--color-emerald)" : "#e2e8f0" }}>
                        {log}
                      </div>
                    ))}

                    {terminalStep === 3 && (
                      <div style={{ marginTop: "4px" }}>
                        <span className="terminal-prompt">root@server:~$</span>
                        <span className="terminal-cursor" />
                      </div>
                    )}
                  </div>
                </div>

                <div style={{ display: "flex", gap: "0.5rem" }}>
                  <button 
                    onClick={runInstallDemo} 
                    className="btn btn-primary" 
                    style={{ flex: 1, padding: "0.5rem 1rem", fontSize: "0.8rem" }}
                    disabled={terminalStep !== 0}
                  >
                    <Play size={12} />
                    <span>Run Install</span>
                  </button>
                  
                  <button 
                    onClick={resetTerminal} 
                    className="btn btn-secondary" 
                    style={{ padding: "0.5rem", borderRadius: "50%" }}
                    title="Reset Terminal"
                  >
                    <RefreshCw size={12} />
                  </button>
                </div>
              </div>
            </div>

            {/* CELL 4: Bento Features Hub (Takes 7 cols) */}
            <div 
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ gridColumn: "span 7" }}
            >
              <div className="console-card" style={{ flexDirection: "column", gap: "1.5rem" }}>
                <div>
                  <h3 style={{ fontSize: "1.3rem", marginBottom: "0.5rem" }}>System Architecture Highlights</h3>
                  <p style={{ color: "var(--text-secondary)", fontSize: "0.9rem" }}>
                    CypherPanel splits logic layers to provide high availability and zero-footprint hosting nodes.
                  </p>
                </div>

                <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "1rem" }}>
                  
                  <div className="glass-panel" style={{ padding: "1.25rem", border: "1px solid rgba(255,255,255,0.04)" }}>
                    <div style={{ display: "flex", alignItems: "center", gap: "0.5rem", marginBottom: "0.5rem", color: "var(--color-indigo)" }}>
                      <Cpu size={16} />
                      <strong style={{ fontSize: "0.85rem", color: "#fff" }}>cgroups v2 Isolation</strong>
                    </div>
                    <span style={{ fontSize: "0.8rem", color: "var(--text-secondary)", lineHeight: "1.5" }}>
                      Sandbox client accounts inside OS-level resource limits, throttling RAM and CPU spikes.
                    </span>
                  </div>

                  <div className="glass-panel" style={{ padding: "1.25rem", border: "1px solid rgba(255,255,255,0.04)" }}>
                    <div style={{ display: "flex", alignItems: "center", gap: "0.5rem", marginBottom: "0.5rem", color: "var(--color-violet)" }}>
                      <Zap size={16} />
                      <strong style={{ fontSize: "0.85rem", color: "#fff" }}>Zero-Downtime Migration</strong>
                    </div>
                    <span style={{ fontSize: "0.8rem", color: "var(--text-secondary)", lineHeight: "1.5" }}>
                      Move virtual hosts between server clusters dynamically by re-routing proxy channels.
                    </span>
                  </div>

                  <div className="glass-panel" style={{ padding: "1.25rem", border: "1px solid rgba(255,255,255,0.04)" }}>
                    <div style={{ display: "flex", alignItems: "center", gap: "0.5rem", marginBottom: "0.5rem", color: "var(--color-emerald)" }}>
                      <Shield size={16} />
                      <strong style={{ fontSize: "0.85rem", color: "#fff" }}>DMARC Aggregation</strong>
                    </div>
                    <span style={{ fontSize: "0.8rem", color: "var(--text-secondary)", lineHeight: "1.5" }}>
                      Ingest DMARC reports natively, flagging malicious spoofing attempts across your DNS zones.
                    </span>
                  </div>

                  <div className="glass-panel" style={{ padding: "1.25rem", border: "1px solid rgba(255,255,255,0.04)" }}>
                    <div style={{ display: "flex", alignItems: "center", gap: "0.5rem", marginBottom: "0.5rem", color: "var(--color-amber)" }}>
                      <GitBranch size={16} />
                      <strong style={{ fontSize: "0.85rem", color: "#fff" }}>Atomic Rollbacks</strong>
                    </div>
                    <span style={{ fontSize: "0.8rem", color: "var(--text-secondary)", lineHeight: "1.5" }}>
                      Every git push deploys isolated folders. Rollback immediately with symbolic link cutovers.
                    </span>
                  </div>

                </div>
              </div>
            </div>

            {/* CELL 5: Backup restoration timeline (Takes 6 cols) */}
            <div 
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ gridColumn: "span 6" }}
            >
              <div className="console-card" style={{ flexDirection: "column", gap: "1.5rem" }}>
                <div>
                  <h3 style={{ fontSize: "1.3rem", marginBottom: "0.5rem" }}>Borg & Restic Backup Vault</h3>
                  <p style={{ color: "var(--text-secondary)", fontSize: "0.9rem" }}>
                    Deduplicated snapshots sent automatically to S3 target buckets with chronological restoration.
                  </p>
                </div>

                {/* Restoration timeline mockup */}
                <div style={{ display: "flex", flexDirection: "column", gap: "0.75rem" }}>
                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "1rem", fontSize: "0.8rem" }}>
                    <span style={{ color: "var(--color-emerald)" }}>● Daily Snapshot #42 (Success)</span>
                    <div style={{ height: "1px", background: "rgba(255,255,255,0.1)", flex: 1 }} />
                    <button className="badge badge-indigo" style={{ border: "none", cursor: "pointer" }}>Restore</button>
                  </div>
                  
                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "1rem", fontSize: "0.8rem" }}>
                    <span style={{ color: "var(--text-secondary)" }}>● Daily Snapshot #41 (Success)</span>
                    <div style={{ height: "1px", background: "rgba(255,255,255,0.05)", flex: 1 }} />
                    <span style={{ color: "var(--text-muted)" }}>2 days ago</span>
                  </div>

                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "1rem", fontSize: "0.8rem" }}>
                    <span style={{ color: "var(--text-secondary)" }}>● Daily Snapshot #40 (Success)</span>
                    <div style={{ height: "1px", background: "rgba(255,255,255,0.05)", flex: 1 }} />
                    <span style={{ color: "var(--text-muted)" }}>3 days ago</span>
                  </div>
                </div>
              </div>
            </div>

            {/* CELL 6: Technology Badge Cloud (Takes 6 cols) */}
            <div 
              className="mouse-glow-card" 
              onMouseMove={handleMouseMove}
              style={{ gridColumn: "span 6" }}
            >
              <div className="console-card" style={{ flexDirection: "column", gap: "1.5rem" }}>
                <div>
                  <h3 style={{ fontSize: "1.3rem", marginBottom: "0.5rem" }}>Engineered System Stack</h3>
                  <p style={{ color: "var(--text-secondary)", fontSize: "0.9rem" }}>
                    Compiled for low CPU bounds and zero memory leaks. No bloated runtimes.
                  </p>
                </div>

                <div className="badge-grid">
                  <div className="tech-tag"><Server size={14} /> Go (Golang)</div>
                  <div className="tech-tag"><Globe size={14} /> Next.js 16</div>
                  <div className="tech-tag"><Sliders size={14} /> Gin Gonic</div>
                  <div className="tech-tag"><Database size={14} /> PostgreSQL</div>
                  <div className="tech-tag"><Key size={14} /> Redis</div>
                  <div className="tech-tag"><Cpu size={14} /> cgroups v2</div>
                  <div className="tech-tag"><Zap size={14} /> NATS JetStream</div>
                  <div className="tech-tag"><Shield size={14} /> gRPC & mTLS</div>
                </div>
              </div>
            </div>

          </div>

        </div>
      </section>

      <Footer />
    </>
  );
}
