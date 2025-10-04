// src/components/Navbar.jsx
import React from "react";

const Navbar = ({ user, onLoginClick }) => {
  return (
    <nav className="w-full bg-blue-800 text-white flex items-center justify-between px-6 py-4 min-w-[600px] border-b-4 border-blue-700">
      {/* Left: Home button + divider */}
      <div className="flex items-center flex-shrink-0">
        <button className="text-lg font-semibold px-4 py-2 rounded hover:bg-blue-700 transition-colors">
          Home
        </button>
        <div className="w-px h-8 bg-blue-700 mx-2"></div>
      </div>

      {/* Center: Database button, dividers, search bar + magnifying icon */}
      <div className="flex items-center flex-grow justify-center space-x-4">
        <div className="flex items-center">
          <button className="text-lg px-4 py-2 rounded hover:bg-blue-700 transition-colors">
            📚
          </button>
          <div className="w-px h-8 bg-blue-700 mx-2"></div>
        </div>

        <div className="flex items-center bg-blue-700 rounded-md px-3 py-2 flex-grow max-w-[500px] hover:bg-blue-600 transition-colors">
          <span className="mr-2">🔍</span>
          <input
            type="text"
            placeholder="Search..."
            className="flex-grow bg-transparent text-white placeholder-blue-200 text-base h-full outline-none"
          />
        </div>
        <div className="w-px h-8 bg-blue-700 mx-2"></div>
      </div>

      {/* Right: Login/profile + divider */}
      <div className="flex items-center flex-shrink-0 space-x-4">
        <div className="w-px h-8 bg-blue-700"></div>
        <img
          src="/blank-profile.png" // dynamically replace when logged in
          alt="Profile"
          className="w-10 h-10 rounded-full"
        />
        <button className="text-base font-medium px-4 py-2 rounded hover:bg-blue-700 transition-colors">
          Login
        </button>
      </div>
    </nav>
  );
};

export default Navbar;
