"use client";

import React, { useRef, useEffect, useState } from "react";
import * as THREE from "three";
import { Server, Activity, ShieldAlert, Cpu } from "lucide-react";
import { motion } from "framer-motion";

export default function ThreeDHub() {
  const canvasRef = useRef(null);
  const containerRef = useRef(null);
  
  // Interactive React States
  const [trafficSpike, setTrafficSpike] = useState(false);
  const [secureMode, setSecureMode] = useState(false);
  const [activeNodeCount, setActiveNodeCount] = useState(120);

  // Internal APIs to bridge React UI actions to Three.js render loop
  const triggerSpikeRef = useRef(null);
  const toggleSecureRef = useRef(null);
  const addNodeRef = useRef(null);

  // Mouse Move tracking for parallax tilt
  const mouse = useRef({ x: 0, y: 0 });
  const handleMouseMove = (e) => {
    if (!containerRef.current) return;
    const rect = containerRef.current.getBoundingClientRect();
    mouse.current.x = (e.clientX - rect.left) / rect.width - 0.5;
    mouse.current.y = (e.clientY - rect.top) / rect.height - 0.5;
  };

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    // Dimensions
    let width = canvas.parentElement.clientWidth;
    let height = canvas.parentElement.clientHeight || 450;

    // Scene setup
    const scene = new THREE.Scene();
    
    // Perspective Camera
    const camera = new THREE.PerspectiveCamera(45, width / height, 0.1, 100);
    camera.position.z = 10;

    // WebGL Renderer
    const renderer = new THREE.WebGLRenderer({
      canvas,
      antialias: true,
      alpha: true
    });
    renderer.setSize(width, height);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));

    // Base Group to hold all 3D mesh elements
    const networkGroup = new THREE.Group();
    scene.add(networkGroup);

    // Grid Coordinates Generation
    const maxNodes = 200;
    const initialRadius = 3;
    const nodes = [];
    const basePositions = new Float32Array(maxNodes * 3);
    const currentPositions = new Float32Array(maxNodes * 3);
    const nodeVelocities = new Float32Array(maxNodes * 3);

    for (let i = 0; i < maxNodes; i++) {
      // Uniform spherical coordinate distribution
      const u = Math.random();
      const v = Math.random();
      const theta = u * 2.0 * Math.PI;
      const phi = Math.acos(2.0 * v - 1.0);
      
      const x = initialRadius * Math.sin(phi) * Math.cos(theta);
      const y = initialRadius * Math.sin(phi) * Math.sin(theta);
      const z = initialRadius * Math.cos(phi);

      basePositions[i * 3] = x;
      basePositions[i * 3 + 1] = y;
      basePositions[i * 3 + 2] = z;

      currentPositions[i * 3] = x;
      currentPositions[i * 3 + 1] = y;
      currentPositions[i * 3 + 2] = z;

      // Velocities for traffic spikes
      nodeVelocities[i * 3] = (Math.random() - 0.5) * 0.1;
      nodeVelocities[i * 3 + 1] = (Math.random() - 0.5) * 0.1;
      nodeVelocities[i * 3 + 2] = (Math.random() - 0.5) * 0.1;

      nodes.push({ x, y, z, active: i < activeNodeCount });
    }

    // Nodes Buffer Geometry
    const nodesGeometry = new THREE.BufferGeometry();
    nodesGeometry.setAttribute("position", new THREE.BufferAttribute(currentPositions, 3));

    // Material (Indigo by default, Emerald when Secure)
    const pointsMaterial = new THREE.PointsMaterial({
      size: 0.12,
      color: 0x6366f1,
      transparent: true,
      opacity: 0.85
    });

    const nodePoints = new THREE.Points(nodesGeometry, pointsMaterial);
    networkGroup.add(nodePoints);

    // Lines Connecting Nodes Buffer
    const lineIndices = [];
    const maxDistance = 1.4;

    const computeConnections = () => {
      lineIndices.length = 0;
      for (let i = 0; i < maxNodes; i++) {
        if (i >= activeNodeCount) continue;
        for (let j = i + 1; j < maxNodes; j++) {
          if (j >= activeNodeCount) continue;
          
          const dx = currentPositions[i * 3] - currentPositions[j * 3];
          const dy = currentPositions[i * 3 + 1] - currentPositions[j * 3 + 1];
          const dz = currentPositions[i * 3 + 2] - currentPositions[j * 3 + 2];
          const dist = Math.sqrt(dx * dx + dy * dy + dz * dz);

          if (dist < maxDistance) {
            lineIndices.push(i, j);
          }
        }
      }
    };
    computeConnections();

    // Create line segments
    const linesGeometry = new THREE.BufferGeometry();
    linesGeometry.setAttribute("position", new THREE.BufferAttribute(currentPositions, 3));
    linesGeometry.setIndex(lineIndices);

    const linesMaterial = new THREE.LineBasicMaterial({
      color: 0x8b5cf6,
      transparent: true,
      opacity: 0.25,
      linewidth: 1
    });

    const networkLines = new THREE.LineSegments(linesGeometry, linesMaterial);
    networkGroup.add(networkLines);

    // Ambient Lighting
    const ambientLight = new THREE.AmbientLight(0xffffff, 0.4);
    scene.add(ambientLight);

    const directionalLight = new THREE.DirectionalLight(0xffffff, 0.8);
    directionalLight.position.set(5, 5, 5);
    scene.add(directionalLight);

    // UI Action Triggers
    let spikeState = false;
    triggerSpikeRef.current = () => {
      spikeState = true;
      setTimeout(() => {
        spikeState = false;
      }, 3000);
    };

    toggleSecureRef.current = (secure) => {
      pointsMaterial.color.setHex(secure ? 0x10b981 : 0x6366f1);
      linesMaterial.color.setHex(secure ? 0x34d399 : 0x8b5cf6);
    };

    addNodeRef.current = () => {
      setActiveNodeCount((prev) => {
        const next = Math.min(maxNodes, prev + 10);
        for (let i = prev; i < next; i++) {
          nodes[i].active = true;
        }
        return next;
      });
    };

    // Scroll progress tracker
    let scrollRatio = 0;
    const handleScroll = () => {
      const scrollY = window.scrollY;
      const docHeight = document.documentElement.scrollHeight - window.innerHeight;
      scrollRatio = docHeight > 0 ? scrollY / docHeight : 0;
    };
    window.addEventListener("scroll", handleScroll);

    // Resize Handler
    const handleResize = () => {
      if (!canvas || !canvas.parentElement) return;
      width = canvas.parentElement.clientWidth;
      height = canvas.parentElement.clientHeight || 450;
      camera.aspect = width / height;
      camera.updateProjectionMatrix();
      renderer.setSize(width, height);
    };
    window.addEventListener("resize", handleResize);

    // Animation Loop Variables
    let clock = new THREE.Clock();

    // Render loop
    const animate = () => {
      const delta = clock.getDelta();
      const time = clock.getElapsedTime();

      // Parallax Tilt based on mouse
      networkGroup.rotation.y = time * 0.1 + scrollRatio * Math.PI * 1.5 + mouse.current.x * 0.6;
      networkGroup.rotation.x = scrollRatio * Math.PI * 0.5 + mouse.current.y * 0.6;

      // Scroll-Driven Scale
      const scaleVal = 1 + scrollRatio * 0.4;
      networkGroup.scale.set(scaleVal, scaleVal, scaleVal);

      // Node coordinates jitter animation
      const positionAttr = nodesGeometry.attributes.position;
      const positions = positionAttr.array;

      for (let i = 0; i < maxNodes; i++) {
        // Only animate active nodes
        if (i < activeNodeCount) {
          const jitterSpeed = spikeState ? 3.5 : 0.8;
          const jitterAmp = spikeState ? 0.35 : 0.05;

          // Deform coordinates with sine waves
          positions[i * 3] = basePositions[i * 3] + Math.sin(time * jitterSpeed + i) * jitterAmp;
          positions[i * 3 + 1] = basePositions[i * 3 + 1] + Math.cos(time * jitterSpeed + i) * jitterAmp;
          positions[i * 3 + 2] = basePositions[i * 3 + 2] + Math.sin(time * jitterSpeed * 0.8 + i) * jitterAmp;
        } else {
          // Inactive nodes sit collapsed at origin
          positions[i * 3] = 0;
          positions[i * 3 + 1] = 0;
          positions[i * 3 + 2] = 0;
        }
      }
      
      positionAttr.needsUpdate = true;

      // Re-calculate line positions
      computeConnections();
      linesGeometry.setIndex(lineIndices);
      linesGeometry.attributes.position.needsUpdate = true;

      renderer.render(scene, camera);
      animationFrameId = requestAnimationFrame(animate);
    };

    let animationFrameId = requestAnimationFrame(animate);

    // Cleanup
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
  }, [activeNodeCount]);

  // Sync state toggles to Three.js refs
  useEffect(() => {
    if (toggleSecureRef.current) {
      toggleSecureRef.current(secureMode);
    }
  }, [secureMode]);

  const triggerSpike = () => {
    setTrafficSpike(true);
    if (triggerSpikeRef.current) {
      triggerSpikeRef.current();
    }
    setTimeout(() => setTrafficSpike(false), 3000);
  };

  const addServerNode = () => {
    if (addNodeRef.current) {
      addNodeRef.current();
    }
  };

  const easeTransition = [0.16, 1, 0.3, 1];

  return (
    <section className="tech-section" style={{ background: "radial-gradient(circle at center, rgba(16,24,48,0.3) 0%, transparent 80%)", overflow: "hidden" }}>
      <div className="container" ref={containerRef} onMouseMove={handleMouseMove}>
        
        <div className="section-title-wrapper">
          <span className="badge badge-indigo">Interactive WebGL Scene</span>
          <h2 className="section-title">3D Real-time Network Hub</h2>
          <p className="section-subtitle">
            Simulate how CypherCore monitors and coordinates regional CypherAgents dynamically in 3D space.
          </p>
        </div>

        <div className="arch-grid" style={{ gridTemplateColumns: "1fr 1.2fr", alignItems: "center", gap: "3rem" }}>
          
          {/* Interactive controls column */}
          <motion.div 
            initial={{ opacity: 0, x: -30 }}
            whileInView={{ opacity: 1, x: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.8, ease: easeTransition }}
            style={{ display: "flex", flexDirection: "column", gap: "1.5rem" }}
          >
            <div className="glass-panel" style={{ padding: "2.25rem", display: "flex", flexDirection: "column", gap: "1.5rem" }}>
              <h3 className="arch-title" style={{ fontSize: "1.5rem" }}>Agent Fleet Controller</h3>
              <p style={{ fontSize: "0.95rem", color: "var(--text-secondary)" }}>
                Use the controls below to inject traffic triggers or modify secure state overlays and observe the WebGL network response.
              </p>

              <div style={{ display: "flex", flexDirection: "column", gap: "1rem" }}>
                
                {/* Traffic Spike simulator */}
                <button 
                  onClick={triggerSpike}
                  className={`btn ${trafficSpike ? "btn-primary" : "btn-secondary"}`}
                  style={{ justifyContent: "flex-start", padding: "1rem" }}
                  disabled={trafficSpike}
                >
                  <Activity size={18} className={trafficSpike ? "animate-pulse" : ""} color={trafficSpike ? "#fff" : "var(--color-indigo)"} />
                  <div style={{ textAlign: "left", marginLeft: "0.5rem" }}>
                    <span style={{ display: "block", fontSize: "0.9rem", fontWeight: "600" }}>Simulate Traffic Spike</span>
                    <span style={{ display: "block", fontSize: "0.75rem", color: "var(--text-muted)", fontWeight: "normal" }}>
                      {trafficSpike ? "Spike Injected - Network vibrating..." : "Vibrate nodes with high amplitude I/O"}
                    </span>
                  </div>
                </button>

                {/* Secure overlay toggle */}
                <button 
                  onClick={() => setSecureMode(!secureMode)}
                  className={`btn ${secureMode ? "btn-primary" : "btn-secondary"}`}
                  style={{ 
                    justifyContent: "flex-start", 
                    padding: "1rem",
                    background: secureMode ? "linear-gradient(135deg, var(--color-emerald) 0%, #059669 100%)" : "",
                    boxShadow: secureMode ? "0 4px 14px rgba(16, 185, 129, 0.3)" : ""
                  }}
                >
                  <Server size={18} color={secureMode ? "#fff" : "var(--color-emerald)"} />
                  <div style={{ textAlign: "left", marginLeft: "0.5rem" }}>
                    <span style={{ display: "block", fontSize: "0.9rem", fontWeight: "600" }}>
                      {secureMode ? "mTLS Enforced (Secure)" : "Enforce mTLS Encryption"}
                    </span>
                    <span style={{ display: "block", fontSize: "0.75rem", color: "var(--text-muted)", fontWeight: "normal" }}>
                      {secureMode ? "mTLS tunnel greenlit across agent sockets" : "Secure daemon channel sockets globally"}
                    </span>
                  </div>
                </button>

                {/* Add node widget */}
                <button 
                  onClick={addServerNode}
                  className="btn btn-secondary"
                  style={{ justifyContent: "flex-start", padding: "1rem" }}
                  disabled={activeNodeCount >= 200}
                >
                  <Cpu size={18} color="var(--color-amber)" />
                  <div style={{ textAlign: "left", marginLeft: "0.5rem" }}>
                    <span style={{ display: "block", fontSize: "0.9rem", fontWeight: "600" }}>Provision Server Nodes</span>
                    <span style={{ display: "block", fontSize: "0.75rem", color: "var(--text-muted)", fontWeight: "normal" }}>
                      Active Node Count: {activeNodeCount} / 200 (Add +10 nodes)
                    </span>
                  </div>
                </button>

              </div>
            </div>

            {/* Scroll indicator banner */}
            <div className="glass-panel" style={{ padding: "1.25rem 1.75rem", display: "flex", alignItems: "center", gap: "0.75rem" }}>
              <ShieldAlert size={18} color="var(--color-indigo)" style={{ flexShrink: 0 }} />
              <span style={{ fontSize: "0.85rem", color: "var(--text-secondary)" }}>
                <strong>Scroll-Driven Sync:</strong> Rotate and scale this scene on multiple axes by scrolling down the homepage.
              </span>
            </div>

          </motion.div>

          {/* WebGL Canvas column */}
          <motion.div 
            initial={{ opacity: 0, x: 30 }}
            whileInView={{ opacity: 1, x: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.8, ease: easeTransition }}
            style={{ 
              position: "relative", 
              width: "100%", 
              height: "450px", 
              display: "flex", 
              alignItems: "center", 
              justifyContent: "center" 
            }}
          >
            {/* Custom glowing backdrop for 3D globe */}
            <div style={{
              position: "absolute",
              width: "70%",
              height: "70%",
              borderRadius: "50%",
              background: secureMode ? "radial-gradient(circle, rgba(16, 185, 129, 0.08) 0%, transparent 70%)" : "radial-gradient(circle, rgba(99, 102, 241, 0.08) 0%, transparent 70%)",
              filter: "blur(20px)",
              pointerEvents: "none",
              zIndex: 0
            }} />
            <canvas ref={canvasRef} style={{ width: "100%", height: "100%", zIndex: 10, cursor: "grab" }} />
          </motion.div>

        </div>

      </div>
    </section>
  );
}
